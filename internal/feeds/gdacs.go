package feeds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stepanok/beacon-server/internal/model"
)

// GDACS pulls the current multi-hazard event list (earthquake, tropical cyclone,
// flood, wildfire, drought, volcano) from the Global Disaster Alert & Coordination
// System (JRC + UN OCHA). Free, no key. Carries GLIDE ids + alert levels.
type GDACS struct{ url string }

func NewGDACS(url string) *GDACS {
	if url == "" {
		url = "https://www.gdacs.org/gdacsapi/api/events/geteventlist/MAP"
	}
	return &GDACS{url: url}
}

func (g *GDACS) Name() string { return "GDACS" }

type gdacsResp struct {
	Features []struct {
		Bbox     []float64 `json:"bbox"`
		Geometry struct {
			Type        string          `json:"type"`
			Coordinates json.RawMessage `json:"coordinates"` // Point [lng,lat] OR Polygon nested arrays
		} `json:"geometry"`
		Properties struct {
			Eventtype  string `json:"eventtype"`
			Eventid    int64  `json:"eventid"`
			Glide      string `json:"glide"`
			Name       string `json:"name"`
			Country    string `json:"country"`
			Alertlevel string `json:"alertlevel"`
			Fromdate   string `json:"fromdate"`
			Todate     string `json:"todate"`
			Iscurrent  string `json:"iscurrent"`
		} `json:"properties"`
	} `json:"features"`
}

// gdacsNature maps GDACS event types to our crisis-nature vocabulary (free text;
// volcano/drought have no direct enum but are stored verbatim).
func gdacsNature(t string) string {
	switch strings.ToUpper(t) {
	case "EQ":
		return "earthquake"
	case "TC":
		return "hurricane" // tropical cyclone
	case "FL":
		return "flood"
	case "WF":
		return "wildfire"
	case "TS":
		return "tsunami"
	case "VO":
		return "volcano"
	case "DR":
		return "drought"
	default:
		return strings.ToLower(t)
	}
}

const gdacsDateLayout = "2006-01-02T15:04:05"

func parseGdacsDate(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(gdacsDateLayout, s)
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

func (g *GDACS) Fetch(ctx context.Context, client *http.Client) ([]model.Crisis, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gdacs http %d", resp.StatusCode)
	}
	var data gdacsResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	out := make([]model.Crisis, 0, len(data.Features))
	for _, f := range data.Features {
		p := f.Properties
		// Significant events only — Green is informational noise at global scale.
		if p.Alertlevel != "Orange" && p.Alertlevel != "Red" {
			continue
		}
		if p.Eventid == 0 {
			continue
		}
		lng, lat, ok := gdacsPoint(f.Bbox, f.Geometry.Type, f.Geometry.Coordinates)
		if !ok {
			continue
		}

		started, ok := parseGdacsDate(p.Fromdate)
		if !ok {
			started = time.Now().UTC()
		}
		var ended *time.Time
		if t, ok := parseGdacsDate(p.Todate); ok {
			ended = &t
		}
		// Feed detections are born 'proposed' (see usgs.go): ground reports or an
		// analyst activate them. Non-current GDACS events arrive already closed.
		status := "proposed"
		if strings.EqualFold(p.Iscurrent, "false") {
			status = "closed"
		}
		var glide *string
		if g := strings.TrimSpace(p.Glide); g != "" {
			glide = ptr(g)
		}
		title := p.Name
		if title == "" {
			title = gdacsNature(p.Eventtype) + " in " + p.Country
		}

		out = append(out, model.Crisis{
			ID:        fmt.Sprintf("gdacs-%s-%d", strings.ToUpper(p.Eventtype), p.Eventid),
			Title:     title,
			Area:      p.Country,
			Nature:    gdacsNature(p.Eventtype),
			CenterLat: lat,
			CenterLng: lng,
			Source:    "feed:GDACS",
			StartedAt: started,
			EndedAt:   ended,
			Glide:     glide,
			RadiusKm:  gdacsRadiusKm(f.Bbox, lat, lng, p.Alertlevel),
			Status:    status,
		})
	}
	return out, nil
}

// gdacsPoint derives a representative point. GDACS geometry is sometimes a Point
// and sometimes a Polygon, but every feature carries a bbox — whose center equals
// the point for Point features and the polygon center otherwise, so we use it.
func gdacsPoint(bbox []float64, typ string, raw json.RawMessage) (lng, lat float64, ok bool) {
	if len(bbox) == 4 {
		return (bbox[0] + bbox[2]) / 2, (bbox[1] + bbox[3]) / 2, true
	}
	if strings.EqualFold(typ, "Point") {
		var c []float64
		if json.Unmarshal(raw, &c) == nil && len(c) >= 2 {
			return c[0], c[1], true
		}
	}
	return 0, 0, false
}

// gdacsRadiusKm derives a coverage radius from the event bbox half-diagonal when
// it spans area, else falls back to an alert-level default.
func gdacsRadiusKm(bbox []float64, lat, lng float64, alert string) float64 {
	if len(bbox) == 4 {
		minLng, minLat, maxLng, maxLat := bbox[0], bbox[1], bbox[2], bbox[3]
		if maxLng > minLng || maxLat > minLat {
			r := haversineKm(minLat, minLng, maxLat, maxLng) / 2
			if r >= 20 {
				return r
			}
		}
	}
	switch alert {
	case "Red":
		return 150
	case "Orange":
		return 80
	default:
		return 30
	}
}
