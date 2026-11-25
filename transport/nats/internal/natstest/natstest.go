// Package natstest runs a NATS server with JetStream in the process, for the tests of the driver and the conformance run of the transport
// port (cmd/natsserver): the real server, embedded, on a loopback port, with its storage in a directory of the caller's. It is not a driver,
// and nothing of it is in the thin main.
package natstest

import (
	"errors"
	"time"

	"github.com/nats-io/nats-server/v2/server"
)

// Options say how the server runs.
type Options struct {
	// Dir is where JetStream keeps its storage. Required.
	Dir string
	// Token, when set, is the token every client must present.
	Token string
}

// Start starts a server with JetStream on a random loopback port and returns its URL and the function that stops it.
func Start(o Options) (url string, stop func(), err error) {
	if o.Dir == "" {
		return "", nil, errors.New("natstest: Options.Dir is required")
	}
	srv, err := server.NewServer(&server.Options{Host: "127.0.0.1", Port: server.RANDOM_PORT, JetStream: true, StoreDir: o.Dir,
		Authorization: o.Token, NoLog: true, NoSigs: true})
	if err != nil {
		return "", nil, err
	}
	go srv.Start()
	if !srv.ReadyForConnections(10 * time.Second) {
		srv.Shutdown()
		return "", nil, errors.New("natstest: the server did not come up in 10 seconds")
	}
	return srv.ClientURL(), func() { srv.Shutdown(); srv.WaitForShutdown() }, nil
}
