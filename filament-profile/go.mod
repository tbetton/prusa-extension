module github.com/tbetton/filament-profile

go 1.26.11

require github.com/prusa3d/prusa-connect/sdk v0.0.0

// The Prusa builder checks out the pinned SDK under .core.
replace github.com/prusa3d/prusa-connect/sdk => ./.core/sdk
