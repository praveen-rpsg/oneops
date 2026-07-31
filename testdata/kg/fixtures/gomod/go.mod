// A miniature module used as E1's golden input.
//
// It lives under testdata so the go tool ignores it: `go list ./...`, `go vet`
// and `go build` in the parent module never see it, and the architecture
// guards skip testdata by name. Nothing here is compiled by the platform build.
module example.test/fixture

go 1.24
