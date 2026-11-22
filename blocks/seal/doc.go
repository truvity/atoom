// Package seal is the seal block of atoom (docs/seal-block.md): data keys per purpose and scope, wrapped through the key port and kept
// in the store port, values sealed and opened under them, keyed hashes and signing by purpose. It moved from sluis key@ (record 0013).
//
// The block is built from the two ports alone. Its host passes in the clients of the ports, declares the purposes, supplies the rules and
// the list of recipients, and serves Block.Handler, in its process (socket.InProcess) or on a socket. The block knows no tenant, grant or
// caller: a scope, an address and an authorization are the host's, and the host's rules decide on them.
package seal
