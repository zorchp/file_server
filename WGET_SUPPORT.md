# wget 递归下载支持说明

记录为了让 `wget -r` 能完整拉取 file_server 内容所做的改动、原因，以及客户端各场景的推荐命令。

## 一、背景问题

原版 file_server 的目录页是 AngularJS 客户端渲染的（`data/main.html`），HTML 源码里只有 `{{ item.url }}`、`{{ item.Name }}` 这类占位符，真实文件列表由浏览器 JS 请求 `?format=json` 后动态生成。

wget/curl 不执行 JS，导致：

1. 拿到的 HTML 里没有任何真实链接，递归下载永远为空
2. wget 把占位符当链接去请求，产生 `/{{ item.url }}`、`/href` 等 404
3. 请求 `/{{ item.Name }}/?format=zip` 时服务端直接 panic，连接被断，wget 报「没有接收到数据」
4. 目录里若真实存在 `index.html`，既下不下来又和 wget 本地保存的目录页同名（当前使用场景没有这种文件，但顺手一起修了）

## 二、服务端改动

### 1. 新增 `dirhtml.go`：面向非浏览器客户端的服务端渲染列表

为什么：wget 需要真实 `<a href>` 才能递归。原来的 JSON 接口只有 JS 能用。

要点：

- `wantsPlainHTML(r)`：`Accept` 头不含 `text/html` 时判定为非浏览器（wget/curl 发的是 `*/*`），或显式 `?format=html` 强制
- 目录项名字和 href 都补尾 `/`，wget 只把尾斜杠的链接当目录继续递归
- href 用 `(&url.URL{Path: name}).String()` 转义，空格、中文、特殊字符不会截断链接
- 软链指向目录时按目录输出，与 Web UI / JSON 行为保持一致
- 列表响应加 `Cache-Control: no-store, no-cache, must-revalidate` 和 `Pragma: no-cache`，避免中间代理返回旧列表导致文件缺失

### 2. `main.go` handleDir：插入纯 HTML 分支

位置在 `format=json` / `format=zip` 之后、Angular 模板之前。浏览器请求（`Accept: text/html`）行为完全不变，Web UI 不受影响。目录不存在时返回 404，不再吐一个空模板。

### 3. `main.go` 新增 `serveFileRaw()`，替换 `http.ServeFile`

为什么：Go 标准库的 `http.ServeFile` 对以 `/index.html` 结尾的请求会强制 `301 Location: ./`（标准库既有行为），且会走条件请求返回 304。后果是：

- 目录里真实的 `index.html` 永远下不下来，只会被跳回目录列表
- wget 把生成的目录列表也存成本地 `index.html`，两者撞名；再配合 `-R "index.html*"` 就被一起删掉
- 客户端或中间代理带条件头时可能拿到 304，导致文件内容没更新

改法：`os.Open` + `http.ServeContent`，不做隐式跳转；请求路径是目录时才 301 补尾斜杠（交给 handleDir）；同时删除请求里的 `If-Modified-Since` 和 `If-None-Match`，永不返回 304。`ServeContent` 保留 Range 支持，断点续传仍可用。

### 4. `dirzip.go`：修 panic

`filepath.Walk` 在路径不可访问时以 `walkFn(path, nil, err)` 回调，原代码直接 `info.IsDir()` → nil pointer dereference，`net/http` 捕获 panic 后断连。

改法：回调开头判断 `err != nil` 就跳过该路径；`Get()` 开头加 `os.Stat`，目录不存在直接返回错误（HTTP 500），不再崩溃断连。

### 5. `main.go` 新增 `listenFreePort()`：端口自动探测

为什么：多人/多实例在同一台机器上跑时端口经常被占，启动直接失败。

改法：`-port` 默认值改成 `:8081`，用 `net.Listen` 真实绑定而不是先探测再绑（避免探测和绑定之间被别的进程抢走），失败就 `+1` 换下一个端口，最多 `MAX_PORT_RETRIES = 100` 次；`http.ListenAndServe` 换成 `http.Serve(listener, mux)`；日志打印实际绑定到的端口。指定 `-port=:8092` 时也走同一套逻辑，即从 8092 往后找。

```
Port 18100 not available (listen tcp :18100: bind: address already in use), trying next one..
Listening on port [::]:18101 .....
Recursive download: wget -r -l100 -np -nH -e robots=off -R "index.html*" http://HOSTNAME:18101/
```

绑定成功后 `logWgetCmd()` 会用 `os.Hostname()` 和实际端口拼出可直接复制的递归下载命令，省掉手工对端口。开了 `-auth` 时会带上 `--user=<用户名> --password=***`，密码不落日志，自己替换。

注意：非「端口占用」类错误（如绑 1024 以下端口权限不足）也会触发重试，此时会连着刷 100 行日志后退出，看日志里的报错原因即可。

## 三、客户端 wget 配置

编译并启动：

```bash
go build -o file_server .          # 注意 go build ./... 不产出可执行文件
./file_server -dir=/home/work -depth=100          # 端口从 8081 开始自动探测
./file_server -dir=/home/work -port=:8092 -depth=100   # 从 8092 开始探测
```

启动日志里的 `Listening on port [::]:xxxx` 是实际端口，wget 命令里的 `HOST:8092` 要按它替换。

### 场景 1：普通递归下载（默认推荐）

远端目录树里不存在真实 `index.html` 的情况，这是常规用法：

```bash
wget -r -l100 -np -nH -e robots=off -R "index.html*" \
  http://HOST:8092/PATH/
```

- `-r -l100` 递归，深度 100
- `-np` 不回溯父目录
- `-nH` 本地不建 `HOST:8092/` 这层目录
- `-e robots=off` 忽略服务端 robots.txt
- `-R "index.html*"` 解析完目录页后删掉它，只留真实文件

本地即使残留着上一次跑剩的、内容不全的 `index.html`，也不影响结果：wget 不会读本地文件，每次都重新请求 URL，解析新下载的那份列表。实测（远端有 `a.txt`、`b.txt`、`sub/c.txt`，本地预置只列了 `a.txt` 的残缺 `index.html`）三个文件全部拉全。

**唯一禁忌：不要加 `-nc` / `--no-clobber`。** 递归模式下本地文件已存在时，wget 会直接读取并解析本地那份当作从网上取回的，残缺列表会决定递归范围。同样条件实测只拉到 `a.txt`。

### 场景 2：远端目录里确实存在真实 index.html

```bash
wget -r -l100 -np -nH -e robots=off \
  --default-page=_list.html -R '_list.html*' \
  http://HOST:8092/PATH/
```

`--default-page` 让 wget 把「以 / 结尾的 URL」存成 `_list.html` 而不是 `index.html`，与真实 `index.html` 彻底分离，再用 `-R '_list.html*'` 只删列表页，避免场景 1 的 `-R "index.html*"` 把真实文件一起删掉。已在 wget 1.12 上验证可用。

### 场景 3：只要整个目录，一把打包

```bash
wget -O PATH.zip 'http://HOST:8092/PATH/?format=zip'
```

不递归、不解析 HTML，服务端边遍历边流式打 zip。目录很大时内存占用低，但没有断点续传。

### 场景 4：断网/中断后续传补拉

```bash
wget -r -l100 -np -nH -e robots=off -c -N -R "index.html*" \
  http://HOST:8092/PATH/
```

- `-c` 续传未下载完的文件
- `-N` 按时间戳跳过未变更的文件。真实文件由 `serveFileRaw()` 返回 `Last-Modified`，跳过生效；目录列表不带 `Last-Modified`，wget 会提示「缺少 Last-modified 文件头 -- 关闭时间戳标记」并照常重新下载，所以列表永远是最新的，不会漏文件

### 场景 5：目录树里有软链，担心成环

```bash
wget -r -l100 -np -nH -e robots=off -R "index.html*" \
  -X '/PATH/软链目录名,/PATH/其它排除目录' \
  http://HOST:8092/PATH/
```

服务端会把「指向目录的软链」当目录列出，如果软链指回自身祖先会形成环，用 `-X` 排除，或把 `-l` 调小。

### 场景 6：开了 Basic Auth

```bash
./file_server -dir=/home/work -port=:8092 -auth='user:pass'
```

```bash
wget -r -l100 -np -nH -e robots=off -R "index.html*" \
  --user=user --password=pass \
  http://HOST:8092/PATH/
```

### 场景 7：只想看列表不下载（调试）

```bash
curl -s 'http://HOST:8092/PATH/'              # 纯 HTML 列表（Accept 非 text/html）
curl -s 'http://HOST:8092/PATH/?format=html'  # 强制纯 HTML 列表
curl -s 'http://HOST:8092/PATH/?format=json'  # JSON，Web UI 用的接口
```

## 四、缓存与残缺列表结论（实测）

- 服务端目录列表：`Cache-Control: no-store, no-cache, must-revalidate` + `Pragma: no-cache`，且不带 `Last-Modified` / `ETag`，代理和条件请求都无法命中，每次都是实时目录内容
- 服务端文件：删掉 `If-Modified-Since` / `If-None-Match`，永不返回 304，始终发完整内容（Range 续传除外）
- 本地残留的残缺 `index.html`：默认模式下无影响，wget 重新请求并解析新下载的列表，文件能拉全
- `-N`：安全。列表因缺 `Last-Modified` 必定重新下载，只有真实文件会按时间戳跳过
- `-nc` / `--no-clobber`：**会出问题**。递归时 wget 直接解析本地已存在的文件，残缺列表会限制递归范围，实测三个文件只拉到一个

## 五、兼容性

- 浏览器访问（`Accept: text/html`）仍然是原来的 AngularJS UI，功能无变化
- `?format=json`、`?format=zip`、上传、命令执行等接口行为不变
- 只有非浏览器客户端会看到新的纯 HTML 列表
