package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/certs"
	"github.com/nietthijmen/bellhop/internal/ipc"
)

func (d *daemon) registerCertRoutes(mux *http.ServeMux) {
	mux.HandleFunc(api.RouteCerts, d.handleCerts)
	mux.HandleFunc(api.RouteCertsExport, d.handleCertsExport)
}

func (d *daemon) handleCerts(w http.ResponseWriter, r *http.Request) {
	info, err := certs.Inspect(d.dirs.Data)
	if err != nil {
		d.writeErr(w, r, certErr(err))
		return
	}
	ipc.WriteJSON(w, http.StatusOK, certView(info))
}

func (d *daemon) handleCertsExport(w http.ResponseWriter, r *http.Request) {
	var req api.CertExportRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		d.writeErr(w, r, badRequest("export body is not valid JSON", "send {\"path\":\"...\"} or an empty body"))
		return
	}
	dest := req.Path
	if dest != "" {
		abs, err := filepath.Abs(dest)
		if err != nil || !filepath.IsAbs(abs) {
			d.writeErr(w, r, badRequest("export path must be absolute", "pass an absolute file path"))
			return
		}
		dest = abs
	}
	info, err := certs.Export(d.dirs.Data, dest)
	if err != nil {
		d.writeErr(w, r, certErr(err))
		return
	}
	ipc.WriteJSON(w, http.StatusOK, api.CertExport{
		Certs:   certView(info),
		PEM:     string(info.PEM),
		Written: dest,
	})
}

func certView(info certs.Info) api.Certs {
	return api.Certs{
		CA:          certs.Authority,
		Path:        info.Path,
		Fingerprint: info.Fingerprint,
		Expires:     info.Expires,
		Trusted:     info.Trusted,
	}
}

func certErr(err error) error {
	if errors.Is(err, certs.ErrMissing) {
		return &api.Error{
			Code:    api.CodeNotFound,
			Message: err.Error(),
			Hint:    "the local CA is created when the proxy starts with HTTPS",
		}
	}
	return err
}
