package mcpdb

import "github.com/townsendmerino/ken/internal/db"

// ListenNotifyScript re-exports internal/db.ListenNotifyScript (the
// embedded SQL setup script for v0.8.0 Part 1 LISTEN/NOTIFY) so SDK
// authors can expose a "print-listen-script" subcommand from their
// own CLI without depending on internal/db directly.
//
// The internal/ convention exists precisely so the package's surface
// can change without breaking external callers; re-exporting the
// variable here lets SDK authors get the script bytes via a stable
// mcp/db public API. Same pattern cmd/ken-mcp uses for its own
// "print-listen-script" subcommand.
//
// Typical SDK author usage:
//
//	func main() {
//	    if len(os.Args) > 1 && os.Args[1] == "print-listen-script" {
//	        _, _ = io.WriteString(os.Stdout, mcpdb.ListenNotifyScript)
//	        return
//	    }
//	    // ... rest of mcp.Run setup ...
//	}
//
// Then operators of the SDK author's binary run:
//
//	mybin print-listen-script | psql $MY_DB_DSN
var ListenNotifyScript = db.ListenNotifyScript
