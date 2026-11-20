module github.com/tbetton/print-queue

go 1.26.11

require (
	github.com/bluenviron/gortsplib/v5 v5.6.7
	github.com/oops1/go.264 v1.12.0
	github.com/pion/rtp v1.10.5
	github.com/prusa3d/prusa-connect/sdk v0.0.0
)

require (
	github.com/bluenviron/mediacommon/v2 v2.9.6 // indirect
	github.com/ebitengine/purego v0.9.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.3 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.19 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.1.3 // indirect
	github.com/pion/transport/v5 v5.1.1 // indirect
	golang.org/x/net v0.59.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

// The Prusa builder checks out the pinned SDK under .core.
replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
