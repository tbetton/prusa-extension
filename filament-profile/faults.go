// The codes this module refuses a caller with. Each one is registered with its
// documentation, as the sdk asks.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/prusa3d/prusa-connect/sdk/fault"
)

//nolint:gochecknoglobals // a domain built once at start-up; a const cannot hold one.
var faultDomain = mustDomain("com.github.aux.tbetton.filament-profile")

//nolint:gochecknoglobals // definitions built once at start-up.
var (
	// RouteNotFound is a path this module does not serve.
	RouteNotFound = fault.Register(faultDomain, "ROUTE_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This module does not serve that path",
		"Read openapi.json. Every path in it is relative to the mount the operator gave this module.",
		fault.RetryNever,
		fault.ShowDetail,
		"path",
	))

	// NotReady is a request that arrived before the host sent the settings.
	NotReady = fault.Register(faultDomain, "NOT_READY", http.StatusServiceUnavailable, fault.Document(
		"The module is starting",
		"Try again in a few seconds.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// BadRequest is a body or a query the route cannot read.
	BadRequest = fault.Register(faultDomain, "BAD_REQUEST", http.StatusBadRequest, fault.Document(
		"The request is not one this route reads",
		"Fix the request and send it again.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// NoCatalogue is a request that needs the Prusa profiles before they were downloaded.
	NoCatalogue = fault.Register(faultDomain, "NO_CATALOGUE", http.StatusServiceUnavailable, fault.Document(
		"The Prusa profiles are not downloaded yet",
		"Wait for the first download to end, or refresh the catalogue.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// NoPreset is a preset or a variant the downloaded profiles do not hold.
	NoPreset = fault.Register(faultDomain, "NO_PRESET", http.StatusNotFound, fault.Document(
		"This filament preset is not in the Prusa profiles",
		"Pick another preset, or another printer variant.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// DownloadFailed is a catalogue that could not be fetched or read.
	DownloadFailed = fault.Register(faultDomain, "DOWNLOAD_FAILED", http.StatusBadGateway, fault.Document(
		"The Prusa profiles could not be downloaded",
		"Check the server's connection and catalogue_url, then refresh.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))
)

// mustDomain builds the fault domain at start-up, and stops the process when
// the name is not valid, since nothing can be refused properly without it.
func mustDomain(name string) fault.Domain {
	domain, err := fault.NewDomain(name)
	if err != nil {
		panic(fmt.Sprintf("filament-profile: %s is not a fault domain: %v", name, err))
	}

	return domain
}

// refuse writes one refusal as RFC 9457 frames it.
func refuse(w http.ResponseWriter, r *http.Request, raised *fault.Fault) {
	problem := raised.ProblemJSON(r.URL.Path)

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)

	_ = json.NewEncoder(w).Encode(problem)
}
