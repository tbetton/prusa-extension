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
var faultDomain = mustDomain("com.github.aux.tbetton.print-queue")

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
		"The queue is starting",
		"Try again in a few seconds.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// BadRequest is a body the route cannot read.
	BadRequest = fault.Register(faultDomain, "BAD_REQUEST", http.StatusBadRequest, fault.Document(
		"The request is not one this route reads",
		"Fix the body and send it again.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	OperationDenied = fault.Register(faultDomain, "OPERATION_DENIED", http.StatusForbidden, fault.Document(
		"The operation is not permitted",
		"Ask an administrator for the required permission.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// NoDevice is a printer the caller cannot see.
	NoDevice = fault.Register(faultDomain, "NO_DEVICE", http.StatusNotFound, fault.Document(
		"This printer is not one you can see",
		"Pick a printer from the list.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// NoFile is a file the storage does not hold, or the caller may not read.
	NoFile = fault.Register(faultDomain, "NO_FILE", http.StatusNotFound, fault.Document(
		"This file is not in the storage",
		"Upload the file to the server's storage first.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// JobNotFound is a job id the queue does not hold.
	JobNotFound = fault.Register(faultDomain, "JOB_NOT_FOUND", http.StatusNotFound, fault.Document(
		"This job is not in the queue",
		"Reload the queue.",
		fault.RetryNever,
		fault.ShowDetail,
	))

	// JobRunning is a change to a job that is being sent or printed.
	JobRunning = fault.Register(faultDomain, "JOB_RUNNING", http.StatusConflict, fault.Document(
		"This job is being printed",
		"Stop the print on the printer, or wait for it to end.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	PlateCheckUnavailable = fault.Register(faultDomain, "PLATE_CHECK_UNAVAILABLE", http.StatusConflict, fault.Document(
		"The plate check could not be configured",
		"Check the printer and camera in Print Queue, then try again.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))

	// StoreFailed is a queue the module could not save.
	StoreFailed = fault.Register(faultDomain, "STORE_FAILED", http.StatusInternalServerError, fault.Document(
		"The queue could not be saved",
		"Read the module's log on the server.",
		fault.RetryAllowed,
		fault.ShowDetail,
	))
)

func mustDomain(name string) fault.Domain {
	domain, err := fault.NewDomain(name)
	if err != nil {
		panic(fmt.Sprintf("print-queue: %s is not a fault domain: %v", name, err))
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
