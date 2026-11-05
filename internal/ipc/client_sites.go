package ipc

import (
	"context"
	"errors"
	"net/http"
	"net/url"

	"github.com/nietthijmen/bellhop/internal/api"
)

// errEmptyName and errEmptyVersion guard path parameters: an empty segment
// would address a different route.
var (
	errEmptyName    = errors.New("site name is empty: pass a site name (see 'bellhop sites')")
	errEmptyVersion = errors.New("PHP version is empty: pass a version such as 8.3 (see 'bellhop php list')")
)

// Sites returns every served site plus name conflicts between parked roots.
func (c *Client) Sites(ctx context.Context) (api.SitesResponse, error) {
	var r api.SitesResponse
	err := c.Do(ctx, http.MethodGet, api.Prefix+"/sites", nil, &r)
	return r, err
}

// Park adds path (absolute) as a parked root.
func (c *Client) Park(ctx context.Context, path string) error {
	return c.Do(ctx, http.MethodPost, api.Prefix+"/park", api.ParkRequest{Path: path}, nil)
}

// Unpark removes path (absolute) from the parked roots.
func (c *Client) Unpark(ctx context.Context, path string) error {
	return c.Do(ctx, http.MethodDelete, api.Prefix+"/park", api.ParkRequest{Path: path}, nil)
}

// Parked returns the parked roots.
func (c *Client) Parked(ctx context.Context) ([]string, error) {
	var r api.ParkedResponse
	if err := c.Do(ctx, http.MethodGet, api.Prefix+"/park", nil, &r); err != nil {
		return nil, err
	}
	return r.Paths, nil
}

// Link links a single project directory as a site and returns it.
func (c *Client) Link(ctx context.Context, req api.LinkRequest) (api.Site, error) {
	var s api.Site
	err := c.Do(ctx, http.MethodPost, api.Prefix+"/link", req, &s)
	return s, err
}

// Unlink removes the linked site name.
func (c *Client) Unlink(ctx context.Context, name string) error {
	if name == "" {
		return errEmptyName
	}
	return c.Do(ctx, http.MethodDelete, api.Prefix+"/link/"+url.PathEscape(name), nil, nil)
}

// PatchSite changes per-site settings and returns the updated site.
func (c *Client) PatchSite(ctx context.Context, name string, p api.SitePatch) (api.Site, error) {
	if name == "" {
		return api.Site{}, errEmptyName
	}
	var s api.Site
	err := c.Do(ctx, http.MethodPatch, api.Prefix+"/sites/"+url.PathEscape(name), p, &s)
	return s, err
}

// PHPVersions returns the known PHP versions, installed or not.
func (c *Client) PHPVersions(ctx context.Context) ([]api.PHPVersion, error) {
	var r api.PHPResponse
	if err := c.Do(ctx, http.MethodGet, api.Prefix+"/php", nil, &r); err != nil {
		return nil, err
	}
	return r.Versions, nil
}

// InstallPHP starts installing a PHP version and returns the job (HTTP 202).
func (c *Client) InstallPHP(ctx context.Context, version string) (api.JobRef, error) {
	if version == "" {
		return api.JobRef{}, errEmptyVersion
	}
	var ref api.JobRef
	err := c.Do(ctx, http.MethodPost, api.Prefix+"/php/"+url.PathEscape(version)+"/install", nil, &ref)
	return ref, err
}

// RemovePHP uninstalls a PHP version.
func (c *Client) RemovePHP(ctx context.Context, version string) error {
	if version == "" {
		return errEmptyVersion
	}
	return c.Do(ctx, http.MethodDelete, api.Prefix+"/php/"+url.PathEscape(version), nil, nil)
}

// SetDefaultPHP makes version the global default PHP version.
func (c *Client) SetDefaultPHP(ctx context.Context, version string) error {
	if version == "" {
		return errEmptyVersion
	}
	return c.Do(ctx, http.MethodPost, api.Prefix+"/php/default", api.PHPDefaultRequest{Version: version}, nil)
}
