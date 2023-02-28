package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// mock command that fails tests if error is returned
func mockMainShouldSucceed() int {
	app := newApp()
	err := app.Run(os.Args)
	if err != nil {
		log.WithLevel(zerolog.FatalLevel).Err(err).Msg("Fatal error")
		return 1
	}
	return 0
}

// mock command that fails tests if no error is returned
func mockMainShouldFail() int {
	app := newApp()
	err := app.Run(os.Args)
	if err != nil {
		log.WithLevel(zerolog.FatalLevel).Err(err).Msg("Fatal error")
		return 0
	}
	fmt.Fprintf(os.Stderr, "expected an error but none was returned\n")
	return 1
}

func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"pint.ok":    mockMainShouldSucceed,
		"pint.error": mockMainShouldFail,
	}))
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:           "tests",
		UpdateScripts: os.Getenv("UPDATE_SNAPSHOTS") == "1",
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"http": httpServer,
		},
		Setup: func(env *testscript.Env) error {
			env.Values["mocks"] = &httpMocks{responses: map[string][]httpMock{}}
			return nil
		},
	})
}

func httpServer(ts *testscript.TestScript, neg bool, args []string) {
	mocks := ts.Value("mocks").(*httpMocks)

	if len(args) == 0 {
		ts.Fatalf("! http command requires arguments")
	}
	cmd := args[0]

	switch cmd {
	// http response name /200 200 OK
	case "response":
		if len(args) < 5 {
			ts.Fatalf("! http response command requires '$NAME $PATH $CODE $BODY' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		path := args[2]
		code, err := strconv.Atoi(args[3])
		ts.Check(err)
		body := strings.Join(args[4:], " ")
		mocks.add(name, httpMock{pattern: path, handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, err := w.Write([]byte(body))
			ts.Check(err)
		}})
	case "method":
		if len(args) < 6 {
			ts.Fatalf("! http response command requires '$NAME $METHOD $PATH $CODE $BODY' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		meth := args[2]
		path := args[3]
		code, err := strconv.Atoi(args[4])
		ts.Check(err)
		body := strings.Join(args[5:], " ")
		mocks.add(name, httpMock{pattern: path, method: meth, handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			_, err := w.Write([]byte(body))
			ts.Check(err)
		}})
	// http auth-response name /200 user password 200 OK
	case "auth-response":
		if len(args) < 7 {
			ts.Fatalf("! http response command requires '$NAME $PATH $USER $PASS $CODE $BODY' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		path := args[2]
		user := args[3]
		pass := args[4]
		code, err := strconv.Atoi(args[5])
		ts.Check(err)
		body := strings.Join(args[6:], " ")
		mocks.add(name, httpMock{pattern: path, handler: func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if ok && username == user && password == pass {
				w.WriteHeader(code)
				_, err := w.Write([]byte(body))
				ts.Check(err)
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		}})
	// http response name /200 200 OK
	case "slow-response":
		if len(args) < 6 {
			ts.Fatalf("! http response command requires '$NAME $PATH $DELAY $CODE $BODY' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		path := args[2]
		delay, err := time.ParseDuration(args[3])
		ts.Check(err)
		code, err := strconv.Atoi(args[4])
		ts.Check(err)
		body := strings.Join(args[5:], " ")
		mocks.add(name, httpMock{pattern: path, handler: func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(delay)
			w.WriteHeader(code)
			_, err := w.Write([]byte(body))
			ts.Check(err)
		}})
	// http redirect name /foo/src /dst
	case "redirect":
		if len(args) != 4 {
			ts.Fatalf("! http redirect command requires '$NAME $SRCPATH $DSTPATH' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		srcpath := args[2]
		dstpath := args[3]
		mocks.add(name, httpMock{pattern: srcpath, handler: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", dstpath)
			w.WriteHeader(http.StatusFound)
		}})
	// http start name 127.0.0.1:7088
	case "start":
		if len(args) != 3 {
			ts.Fatalf("! http start command requires '$NAME $LISTEN' args, got [%s]", strings.Join(args, " "))
		}
		name := args[1]
		listen := args[2]

		mux := http.NewServeMux()
		mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var done bool
			for n, mockList := range mocks.responses {
				if n == name {
					for _, mock := range mockList {
						if mock.pattern != "/" && (r.URL.Path != mock.pattern || !strings.HasPrefix(r.URL.Path, mock.pattern)) {
							continue
						}
						if mock.method != "" && mock.method != r.Method {
							continue
						}
						mock.handler(w, r)
						done = true
					}
					break
				}
			}
			if !done {
				w.WriteHeader(http.StatusNotFound)
			}
		}))

		listener, err := net.Listen("tcp", listen)
		ts.Check(err)
		server := &http.Server{Addr: listen, Handler: mux}
		go func() {
			_ = server.Serve(listener)
		}()

		ts.Defer(func() {
			ts.Logf("http server %s shutting down", name)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(ctx)
		})
	default:
		ts.Fatalf("! unknown http command: %v", args)
	}
}

type httpMock struct {
	pattern string
	method  string
	handler func(http.ResponseWriter, *http.Request)
}

type httpMocks struct {
	responses map[string][]httpMock
}

func (m *httpMocks) add(name string, mock httpMock) {
	if _, ok := m.responses[name]; !ok {
		m.responses[name] = []httpMock{}
	}
	m.responses[name] = append(m.responses[name], mock)
}
