package http

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"sync"

	"github.com/pkg/errors"
	trn "github.com/romshark/dgraph_graphql_go/api/transport"
)

// Server represents an HTTP based server transport implementation
type Server struct {
	addrReadWait *sync.WaitGroup
	opts         ServerOptions
	httpSrv      *http.Server
	tls          *ServerTLS
	addr         net.Addr
	onGraphQuery trn.OnGraphQuery
	onAuth       trn.OnAuth
	onRootAuth   trn.OnRootAuth
	onRootSess   trn.OnRootSess
}

// NewServer creates a new unencrypted JSON based HTTP transport.
// Use NewSecure to enable encryption instead
func NewServer(opts ServerOptions) (trn.Server, error) {
	if err := opts.SetDefaults(); err != nil {
		return nil, err
	}

	t := &Server{
		addrReadWait: &sync.WaitGroup{},
		opts:         opts,
	}
	t.httpSrv = &http.Server{
		Addr:    opts.Host,
		Handler: t,
	}

	if opts.TLS != nil {
		t.httpSrv.TLSConfig = opts.TLS.Config
	}

	t.addrReadWait.Add(1)
	return t, nil
}

// Init implements the transport.Transport interface
func (t *Server) Init(
	onGraphQuery trn.OnGraphQuery,
	onAuth trn.OnAuth,
	onRootAuth trn.OnRootAuth,
	onRootSess trn.OnRootSess,
) error {
	if onGraphQuery == nil {
		panic("missing onGraphQuery callback")
	}
	if onAuth == nil {
		panic("missing onAuth callback")
	}
	if onRootAuth == nil {
		panic("missing onRootAuth callback")
	}
	if onRootSess == nil {
		panic("missing onRootSess callback")
	}
	t.onGraphQuery = onGraphQuery
	t.onAuth = onAuth
	t.onRootAuth = onRootAuth
	t.onRootSess = onRootSess
	return nil
}

// Run implements the transport.Transport interface
func (t *Server) Run() error {
	addr := t.httpSrv.Addr
	if addr == "" {
		addr = ":http"
	}
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return errors.Wrap(err, "TCP listener setup")
	}

	t.addr = listener.Addr()
	// Address determined, readers must be unblocked
	t.addrReadWait.Done()

	tcpListener := tcpKeepAliveListener{
		TCPListener:       listener.(*net.TCPListener),
		KeepAliveDuration: t.opts.KeepAliveDuration,
	}

	if t.opts.TLS != nil {
		if err := t.httpSrv.ServeTLS(
			tcpListener,
			t.opts.TLS.CertificateFilePath,
			t.opts.TLS.PrivateKeyFilePath,
		); err != http.ErrServerClosed {
			return err
		}
	} else {
		if err := t.httpSrv.Serve(tcpListener); err != http.ErrServerClosed {
			return err
		}
	}

	return nil
}

// Shutdown implements the transport.Transport interface
func (t *Server) Shutdown(ctx context.Context) error {
	return t.httpSrv.Shutdown(ctx)
}

// ServeHTTP implements the http.Handler interface
func (t *Server) ServeHTTP(resp http.ResponseWriter, req *http.Request) {
	// Authenticate the client by passing the session in the context
	// of the request
	req = t.auth(req)

	switch req.Method {
	case "POST":
		switch req.URL.Path {
		case "/g":
			t.handleGraphQuery(resp, req)
		case "/root":
			t.handleRootAuth(resp, req)
		default:
			// Unsupported path
			http.Error(
				resp,
				http.StatusText(http.StatusNotFound),
				http.StatusNotFound,
			)
		}
	default:
		http.Error(resp, "unsupported method", http.StatusMethodNotAllowed)
	}
}

// Addr returns the host address URL.
// Blocks until the listener is initialized and the actual address is known
func (t *Server) Addr() url.URL {
	t.addrReadWait.Wait()
	hostAddrStr := t.addr.String()
	return url.URL{
		Scheme: "http",
		Host:   hostAddrStr,
		Path:   "/",
	}
}