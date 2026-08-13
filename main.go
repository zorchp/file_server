package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	dir                 string
	port                string
	logging             bool
	depth               int
	auth                string
	debug               bool
	disable_sys_command bool
	rootDir             = "."
)

//var cpuprof string
//commandsFile        string

const MAX_MEMORY = 1 * 1024 * 1024
const VERSION = "1.0"

// how many consecutive ports are probed when the requested one is busy
const MAX_PORT_RETRIES = 100

func main() {

	//fmt.Println(len(os.Args), os.Args)
	if len(os.Args) > 1 && os.Args[1] == "-v" {
		fmt.Println("Version " + VERSION + " pengzongyu bugfix 20260813")
		os.Exit(0)
	}

	flag.StringVar(&dir, "dir", ".", "Specify a directory to server files from.")
	flag.StringVar(&port, "port", ":8081", "Port to bind the file server, next free one is used if busy")
	flag.BoolVar(&logging, "log", true, "Enable Log (true/false)")
	flag.StringVar(&auth, "auth", "", "'username:pass' Basic Auth")
	flag.IntVar(&depth, "depth", 100, "Depth directory crawler")
	//flag.StringVar(&commandsFile, "commands", "", "Path to external commands file.json")
	flag.BoolVar(&debug, "debug", false, "Make external assets expire every request")
	flag.BoolVar(&disable_sys_command, "disable_cmd", true, "Disable sys comands")

	//flag.StringVar(&cpuprof, "cpuprof", "", "write cpu and mem profile")
	flag.Parse()

	envDir := os.Getenv("FILESERVER_DIR")
	if envDir != "" {
		dir = envDir
	}
	envPort := os.Getenv("FILESERVER_PORT")
	if envPort != "" {
		port = envPort
	}
	envAuth := os.Getenv("FILESERVER_AUTH")
	if envAuth != "" {
		auth = envAuth
	}
	envCmd := os.Getenv("FILESERVER_COMMAND")
	if envCmd != "" {
		disable_sys_command = false
	}

	if logging == false {
		log.SetOutput(ioutil.Discard)
	}
	// If no path is passed to app, normalize to path formath
	if dir == "." {
		dir, _ = filepath.Abs(dir)
	}

	if _, err := os.Stat(dir); err != nil {
		log.Fatalf("Directory %s not exist", dir)
	}

	// normalize dir, ending with... /
	if strings.HasSuffix(dir, "/") == false {
		dir = dir + "/"
	}

	// build index files in background
	go Build_index(dir)

	mux := http.NewServeMux()

	statics := &ServeStaticFromBinary{
		MountPoint: "/-/assets/",
		DataDir:    "data/"}

	mux.Handle("/-/assets/", makeGzipHandler(statics.ServeHTTP))

	mux.Handle("/-/api/dirs", makeGzipHandler(http.HandlerFunc(SearchHandle)))
	mux.Handle("/", BasicAuth(http.HandlerFunc(handleReq), auth))

	listener, err := listenFreePort(port)
	if err != nil {
		log.Fatalf("Cant bind any port in %d tries starting at %s: %s", MAX_PORT_RETRIES, port, err)
	}

	log.Printf("Listening on port %s .....\n", listener.Addr().String())
	logWgetCmd(listener)
	if debug {
		log.Print("Serving data dir in debug mode.. no assets caching.\n")
	}
	http.Serve(listener, mux)

}

// logWgetCmd prints a ready to copy wget command, with the real hostname and
// bound port, to recursively download the served dir
func logWgetCmd(listener net.Listener) {

	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "localhost"
	}

	boundPort := ""
	if tcpAddr, ok := listener.Addr().(*net.TCPAddr); ok {
		boundPort = strconv.Itoa(tcpAddr.Port)
	} else if _, p, err := net.SplitHostPort(listener.Addr().String()); err == nil {
		boundPort = p
	}

	opts := `-r -l100 -np -nH -e robots=off -R "index.html*"`
	if auth != "" {
		opts += " --user=" + strings.SplitN(auth, ":", 2)[0] + " --password=***"
	}

	log.Printf("Recursive download: wget %s http://%s:%s/", opts, host, boundPort)

}

// listenFreePort binds the requested addr, and if the port is already in use,
// retries with the next one, up to MAX_PORT_RETRIES times. Binding instead of
// just probing avoids racing with other processes for the same port.
func listenFreePort(addr string) (net.Listener, error) {

	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	basePort, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}

	var lastErr error
	for i := 0; i < MAX_PORT_RETRIES; i++ {
		p := basePort + i
		listener, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(p)))
		if err == nil {
			return listener, nil
		}
		log.Printf("Port %d not available (%s), trying next one..", p, err)
		lastErr = err
	}

	return nil, lastErr

}

func handleReq(w http.ResponseWriter, r *http.Request) {

	//Is_Ajax := strings.Contains(r.Header.Get("Accept"), "application/json")
	if r.Method == "PUT" {
		AjaxUpload(w, r)
		return
	}
	if r.Method == "POST" {
		WebCommandHandler(w, r)
		return
	}

	log.Print("Request: ", r.RequestURI)
	// See bug #9. For some reason, don't arrive index.html, when asked it..
	if strings.HasSuffix(r.URL.Path, "/") && r.FormValue("get_file") != "true" {
		log.Printf("Index dir %s", r.URL.Path)
		handleDir(w, r)
	} else {
		log.Printf("downloading file %s", path.Clean(dir+r.URL.Path))
		serveFileRaw(w, r, path.Clean(dir+r.URL.Path))
	}

}

// serveFileRaw serves a file as is. Unlike http.ServeFile it doesn't redirect
// requests ending in /index.html to the dir listing, so index.html files can
// be downloaded too. Client caches are ignored, the full content is always
// sent.
func serveFileRaw(w http.ResponseWriter, r *http.Request, name string) {

	f, err := os.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	if fi.IsDir() {
		// dir urls end with /, so handleDir picks them up
		target := &url.URL{Path: r.URL.Path + "/", RawQuery: r.URL.RawQuery}
		http.Redirect(w, r, target.String(), http.StatusMovedPermanently)
		return
	}

	r.Header.Del("If-Modified-Since")
	r.Header.Del("If-None-Match")
	http.ServeContent(w, r, fi.Name(), fi.ModTime(), f)

}

func handleDir(w http.ResponseWriter, r *http.Request) {

	var d string = ""

	//log.Printf("len %d,, %s", len(r.URL.Path), dir)
	if len(r.URL.Path) == 1 {
		// handle root dir
		d = dir
	} else {
		d += dir + r.URL.Path[1:]
	}

	// handle json format of dir...
	if r.FormValue("format") == "json" {

		w.Header().Set("Content-Type", "application/json")
		result := &DirJson{w, d}
		err := result.Get()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	if r.FormValue("format") == "zip" {
		result := &DirZip{w, d}
		err := result.Get()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	// Non browser clients (wget, curl..) can't run the js ui, serve them a
	// plain listing with real links, so recursive downloads work.
	if wantsPlainHTML(r) {
		result := &DirHTML{w, d, r.URL.Path}
		err := result.Get()
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
		}
		return
	}

	// If we dont receive json param... we are asking, for genric app ui...
	template_file, err := Asset("data/main.html")
	if err != nil {
		log.Fatalf("Cant load template main")
	}

	t := template.Must(template.New("listing").Delims("[%", "%]").Parse(string(template_file)))
	v := map[string]interface{}{
		"Path":        r.URL.Path,
		"version":     VERSION,
		"sys_command": disable_sys_command,
	}
	w.Header().Set("Content-Type", "text/html")
	t.Execute(w, v)

}

func AjaxUpload(w http.ResponseWriter, r *http.Request) {
	reader, err := r.MultipartReader()
	if err != nil {
		fmt.Print(err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pa := r.URL.Path[1:]

	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		var ff string
		if dir != "." {
			ff = dir + pa + part.FileName()
		} else {
			ff = pa + part.FileName()
		}

		dst, err := os.Create(ff)
		defer dst.Close()

		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if _, err := io.Copy(dst, part); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}

	fmt.Fprint(w, "ok")
	return
}
