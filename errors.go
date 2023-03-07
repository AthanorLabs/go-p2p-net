package p2pnet

import (
	"errors"
)

var (
	errInvalidListenIP     = errors.New("invalid ListenIP")
	errNilStream           = errors.New("stream is nil")
	errFailedToBootstrap   = errors.New("failed to bootstrap to any bootnode")
	errCannotConnectToSelf = errors.New("cannot connect to self")
)
