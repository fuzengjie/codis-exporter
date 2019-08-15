package main
import (
	"crypto/subtle"
	"flag"
	"fmt"
	"github.com/fuzengjie/codis_exporter/collector"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
	"gopkg.in/yaml.v1"
	"io/ioutil"
	"net/http"
	"os"
	"strings"
)

const (
	program = "codis_exporter"
)

var (
	versionF       = flag.Bool("version", false, "Print version information and exit.")
	listenAddressF = flag.String("web.listen-address", "0.0.0.0:9403", "Address to listen on for web interface and telemetry")
	metricsPathF   = flag.String("web.metrics-path", "/metrics", "Path under which to expose metrics.")
	codisUrlF      = flag.String("codis.fe", "", "the url which point to codis fe, format: 127.0.0.1:18090")
	namespace      = flag.String("namespace", "codis", "Namespace for metrics")
	authFileF      = flag.String("web.auth-file", "", "Path to YAML file with server_user, server_password options for http basic auth (overrides HTTP_AUTH env var).")
)

var landingPage = []byte(`<html>
<head><title>CODIS exporter</title></head>
<body>
<h1>codis exporter</h1>
<p><a href='` + *metricsPathF + `'>Metrics</a></p>
</body>
</html>
`)

type webAuth struct {
	Username string `yaml:"server_user,omitempty"`
	Password string `yaml:"server_password,omitempty"`
}

type basicAuthHandler struct {
	webAuth
	handler http.HandlerFunc
}

func (h *basicAuthHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, _ := r.BasicAuth()
	usernameOk := subtle.ConstantTimeCompare([]byte(h.Username), []byte(username)) == 1
	passwordOk := subtle.ConstantTimeCompare([]byte(h.Password), []byte(password)) == 1
	if !usernameOk || !passwordOk {
		w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}
	h.handler(w, r)
}

func (h *basicAuthHandler) serverHTTP(w http.ResponseWriter, r *http.Request) {
	username, password, _ := r.BasicAuth()
	usernameOk := subtle.ConstantTimeCompare([]byte(h.Username), []byte(username)) == 1
	passwordOk := subtle.ConstantTimeCompare([]byte(h.Password), []byte(password)) == 1
	if !usernameOk || !passwordOk {
		w.Header().Set("WWW-Authenticate", `Basic realm="metrics"`)
		http.Error(w, "Invalid username or password", http.StatusUnauthorized)
		return
	}
	h.handler(w, r)
}

type logger struct {
	log.Logger
}

func (l logger) Println(v ...interface{}) {
	l.Errorln(v...)
}

// check interfaces
var (
	_ log.Logger      = logger{}
	_ promhttp.Logger = logger{}
)

func prometheusHandler() http.Handler {
	cfg := &webAuth{}
	httpAuth := os.Getenv("HTTP_AUTH")
	switch {
	case *authFileF != "":
		bytes, err := ioutil.ReadFile(*authFileF)
		if err != nil {
			log.Fatal("Cannot read auth file: ", err)
		}
		if err := yaml.Unmarshal(bytes, cfg); err != nil {
			log.Fatal("Cannot parse auth file: ", err)
		}
	case httpAuth != "":
		data := strings.SplitN(httpAuth, ":", 2)
		if len(data) != 2 || data[0] == "" || data[1] == "" {
			log.Fatal("HTTP_AUTH should be formatted as user:password")
		}
		cfg.Username = data[0]
		cfg.Password = data[1]
	}

	handler := promhttp.HandlerFor(prometheus.DefaultGatherer, promhttp.HandlerOpts{
		ErrorLog:      logger{log.Base()},
		ErrorHandling: promhttp.HTTPErrorOnError,
	})
	if cfg.Username != "" && cfg.Password != "" {
		handler = &basicAuthHandler{webAuth: *cfg, handler: handler.ServeHTTP}
		log.Infoln("HTTP basic authentication is enabled")
	}

	return handler
}

func startWebServer() {
	handler := prometheusHandler()
	registerCollector()
	http.Handle(*metricsPathF, handler)
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})
	log.Infof("Starting HTTP server on http://%s%s ...", *listenAddressF, *metricsPathF)
	log.Fatal(http.ListenAndServe(*listenAddressF, nil))
}

func registerCollector() {
	codisCollector, err := collector.NewCodisCollector(*codisUrlF, *namespace)
	if err != nil {
		log.Fatal(err)
	}
	prometheus.MustRegister(codisCollector)
}

func main() {
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s %s exports various Codis metrics in Prometheus format.\n", os.Args[0], version.Version)
		fmt.Fprintf(os.Stderr, "Usage: %s [flags]\n\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "Flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *versionF {
		fmt.Println(version.Print(program))
		os.Exit(0)
	}

	startWebServer()

}
