package core

// pmaasServerAdapter provides an adapter that exposes the PMAAS server to internal components.
// It is not intended for plugins.  They received containerAdapter instances.
// We use an adapter to avoid leaking methods intended for internal use into the "public" view
// of the server.
type pmaasServerAdapter struct {
	pmaas *PMAAS
}

func (p pmaasServerAdapter) ContentPathRoot() string {
	return p.pmaas.config.ContentPathRoot
}
