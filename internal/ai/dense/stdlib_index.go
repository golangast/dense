package dense

import "go/types"

// StdlibSignatures maps fully qualified paths to function/interface signatures.
var StdlibSignatures = map[string]string{
	"io.Reader":               "Read(p []byte) (n int, err error)",
	"io.Writer":               "Write(p []byte) (n int, err error)",
	"fmt.Stringer":            "String() string",
	"encoding/json.Marshaler": "MarshalJSON() ([]byte, error)",
	"net/http.Handler":        "ServeHTTP(w http.ResponseWriter, r *http.Request)",
}

// ResolveStdlibInterface checks if a requested type is a well-known stdlib interface.
func ResolveStdlibInterface(name string) (string, bool) {
	sig, ok := StdlibSignatures[name]
	return sig, ok
}

// StdlibTypeInfo can be used to inspect go/types information for standard library symbols.
func StdlibTypeInfo(name string) (*types.TypeName, bool) {
	// Keep this helper lightweight and compile-time safe for resolver-based tooling.
	return nil, false
}
