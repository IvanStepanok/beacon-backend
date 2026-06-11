package boundary

// COD-AB loader: fetches OCHA Common Operational Datasets — Administrative Boundaries
// (the authoritative source of official P-codes) from HDX and ingests them as source='cod'
// (which ResolveAdmin out-ranks over geoBoundaries/seed). This is what makes Beacon's exports
// natively joinable against the UN/OCHA humanitarian stack.
//
// Source: HDX CKAN `package_show?id=cod-ab-{iso3}` → the GeoJSON resource (a zip of
// {iso3}_admin{0,1,2}.geojson FeatureCollections, WGS84). P-codes live in `adm{n}_pcode`
// (current HDX lowercase schema) or `ADM{n}_PCODE` (legacy/fieldmaps uppercase) — we read both.
// License: CC-BY-IGO (OCHA FISS). Falls back to the deterministic fieldmaps.io mirror if HDX is
// unreachable. Geometry is stream-decoded per feature so the (tens-of-MB) ADM2 layer never lands
// in memory whole.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/stepanok/beacon-server/internal/store"
)

const (
	sourceCOD   = "cod"
	hdxAPIBase  = "https://data.humdata.org/api/3/action/package_show?id=cod-ab-"
	fieldmapsZip = "https://data.fieldmaps.io/cod/originals/%s.geojson.zip"
)

// admin{1,2}.geojson members (skip admin0/adminlines/adminpoints).
var codMemberRe = regexp.MustCompile(`(?i)_admin?[12]\.geojson$`)

type ckanResp struct {
	Success bool `json:"success"`
	Result  struct {
		Resources []struct {
			Format       string `json:"format"`
			URL          string `json:"url"`
			LastModified string `json:"last_modified"`
		} `json:"resources"`
	} `json:"result"`
}

// ensureCOD loads official COD-AB ADM1+ADM2 P-codes for a country (source='cod'). Returns the
// number of areas loaded (0 = country not available on COD / fetch failed → caller falls back to
// geoBoundaries). Idempotent: a no-op once the country's COD layer is present.
func (l *Loader) ensureCOD(ctx context.Context, iso3 string) (int, error) {
	if n, err := l.admin.AreaCountByISO3(ctx, iso3, sourceCOD); err == nil && n > 0 {
		return 0, nil // already present → 0 NEWLY loaded (caller must not re-geocode)
	}
	zipBytes, ver, err := l.fetchCODZip(ctx, iso3)
	if err != nil {
		return 0, err
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return 0, fmt.Errorf("cod %s: open zip: %w", iso3, err)
	}
	loaded := 0
	// Process ADM1 before ADM2 so the parent rows exist first.
	for _, level := range []int{1, 2} {
		for _, f := range zr.File {
			if !codMemberRe.MatchString(f.Name) || !strings.Contains(strings.ToLower(f.Name), fmt.Sprintf("admin%d", level)) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				l.log.Warn("cod: open member failed", "iso3", iso3, "member", f.Name, "err", err)
				continue
			}
			n := l.ingestCODMember(ctx, rc, iso3, level, ver)
			rc.Close()
			loaded += n
		}
	}
	if loaded > 0 {
		l.log.Info("COD-AB loaded", "iso3", iso3, "areas", loaded, "version", ver)
	}
	return loaded, nil
}

// ingestCODMember stream-decodes a {iso3}_admin{level}.geojson FeatureCollection and upserts each
// feature as a source='cod' admin area. Streaming keeps peak memory to ~one feature (ADM2 can be
// tens of MB). Returns the number of rows loaded.
func (l *Loader) ingestCODMember(ctx context.Context, r io.Reader, iso3 string, level int, ver string) int {
	dec := json.NewDecoder(r)
	// Advance to the value of the top-level "features" key.
	if !seekToArray(dec, "features") {
		l.log.Warn("cod: no features array", "iso3", iso3, "level", level)
		return 0
	}
	loaded := 0
	for dec.More() {
		var f geoFeature
		if err := dec.Decode(&f); err != nil {
			l.log.Warn("cod: decode feature failed", "iso3", iso3, "level", level, "err", err)
			break
		}
		if len(f.Geometry) == 0 {
			continue
		}
		pcode := propStr(f.Properties, fmt.Sprintf("adm%d_pcode", level), fmt.Sprintf("ADM%d_PCODE", level))
		name := propStr(f.Properties, fmt.Sprintf("adm%d_name", level), fmt.Sprintf("ADM%d_EN", level), fmt.Sprintf("adm%d_en", level))
		if pcode == "" {
			continue // a real P-code is the whole point — skip rows without one
		}
		if name == "" {
			name = pcode
		}
		var parent *string
		if level == 1 {
			parent = &iso3 // the Natural Earth ADM0 baseline row (pcode = ISO3)
		} else if p := propStr(f.Properties, "adm1_pcode", "ADM1_PCODE"); p != "" {
			parent = &p
		}
		if err := store.UpsertAdminAreaGeoJSON(ctx, l.pool, pcode, level, name, parent, iso3, sourceCOD, ver, f.Geometry); err != nil {
			l.log.Warn("cod: upsert failed", "iso3", iso3, "pcode", pcode, "err", err)
			continue
		}
		loaded++
	}
	return loaded
}

// fetchCODZip discovers + downloads the COD-AB GeoJSON zip for a country. Tries HDX (authoritative,
// freshest) first, then the deterministic fieldmaps.io mirror.
func (l *Loader) fetchCODZip(ctx context.Context, iso3 string) ([]byte, string, error) {
	lower := strings.ToLower(iso3)
	if url, ver, err := l.hdxGeoJSONURL(ctx, lower); err == nil && url != "" {
		if b, err := l.download(ctx, url); err == nil {
			return b, ver, nil
		} else {
			l.log.Warn("cod: HDX download failed, trying fieldmaps", "iso3", iso3, "err", err)
		}
	} else if err != nil {
		l.log.Warn("cod: HDX lookup failed, trying fieldmaps", "iso3", iso3, "err", err)
	}
	b, err := l.download(ctx, fmt.Sprintf(fieldmapsZip, lower))
	if err != nil {
		return nil, "", fmt.Errorf("cod %s: HDX + fieldmaps both failed: %w", iso3, err)
	}
	return b, "cod:fieldmaps", nil
}

// hdxGeoJSONURL resolves the GeoJSON resource download URL via the CKAN package_show API. The
// download URL embeds non-derivable UUIDs, so this lookup is required.
func (l *Loader) hdxGeoJSONURL(ctx context.Context, iso3lower string) (string, string, error) {
	body, err := l.download(ctx, hdxAPIBase+iso3lower)
	if err != nil {
		return "", "", err
	}
	var c ckanResp
	if err := json.Unmarshal(body, &c); err != nil {
		return "", "", err
	}
	if !c.Success {
		return "", "", fmt.Errorf("cod %s: CKAN success=false", iso3lower)
	}
	for _, res := range c.Result.Resources {
		if strings.EqualFold(res.Format, "GeoJSON") && res.URL != "" {
			ver := "cod"
			if res.LastModified != "" {
				ver = "cod:" + res.LastModified[:min(10, len(res.LastModified))]
			}
			return res.URL, ver, nil
		}
	}
	return "", "", fmt.Errorf("cod %s: no GeoJSON resource", iso3lower)
}

func (l *Loader) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.client.Do(req) // default client follows redirects (HDX → S3)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 80<<20)) // COD zips are ~10-30 MB
}

// ── small helpers ──────────────────────────────────────────────────────

// propStr returns the first non-empty string property among the given keys (case variants).
func propStr(props map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := props[k].(string); ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// seekToArray advances a json.Decoder to just inside the array value of the named object key.
func seekToArray(dec *json.Decoder, key string) bool {
	depth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		switch t := tok.(type) {
		case json.Delim:
			if t == '{' || t == '[' {
				depth++
			} else {
				depth--
			}
		case string:
			if depth == 1 && t == key {
				// next token should be the opening '[' of the features array
				if d, err := dec.Token(); err == nil {
					if delim, ok := d.(json.Delim); ok && delim == '[' {
						return true
					}
				}
				return false
			}
		}
	}
}
