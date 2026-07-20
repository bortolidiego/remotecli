package geometry

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeometryRoundTrip(t *testing.T) {
	g := DisplayGeometry{
		Capture:  Rect{0, 0, 2560, 1440},
		Video:    Rect{0, 0, 1280, 720},
		Rotation: 0,
	}
	b, err := json.Marshal(g)
	require.NoError(t, err)
	var loaded DisplayGeometry
	require.NoError(t, json.Unmarshal(b, &loaded))
	require.Equal(t, g, loaded)
}

func TestValidate(t *testing.T) {
	require.NoError(t, DisplayGeometry{Capture: Rect{0, 0, 100, 100}, Video: Rect{0, 0, 50, 50}}.Validate())
	require.Error(t, DisplayGeometry{Capture: Rect{0, 0, 0, 100}, Video: Rect{0, 0, 50, 50}}.Validate())
	require.Error(t, DisplayGeometry{Capture: Rect{0, 0, 100, 100}, Video: Rect{0, 0, 0, 50}}.Validate())
	require.Error(t, DisplayGeometry{Capture: Rect{0, 0, 100, 100}, Video: Rect{0, 0, 50, 50}, Rotation: 45}.Validate())
}

func TestToCaptureNoRotation(t *testing.T) {
	g := DisplayGeometry{Capture: Rect{0, 0, 2560, 1440}, Video: Rect{0, 0, 1280, 720}}
	p := g.ToCapture(Point{0.5, 0.5})
	require.InDelta(t, 1280, p.X, 0.001)
	require.InDelta(t, 720, p.Y, 0.001)
}

func TestToCaptureRotation90(t *testing.T) {
	g := DisplayGeometry{Capture: Rect{0, 0, 2560, 1440}, Video: Rect{0, 0, 1280, 720}, Rotation: 90}
	// ponto canto superior-esquerdo do vídeo -> canto superior-direito da captura
	p := g.ToCapture(Point{0.0, 0.0})
	require.InDelta(t, 2560, p.X, 0.001)
	require.InDelta(t, 0, p.Y, 0.001)

	p2 := g.ToCapture(Point{1.0, 1.0})
	require.InDelta(t, 0, p2.X, 0.001)
	require.InDelta(t, 1440, p2.Y, 0.001)
}

func TestToCaptureClips(t *testing.T) {
	g := DisplayGeometry{Capture: Rect{0, 0, 2560, 1440}, Video: Rect{0, 0, 1280, 720}}
	p := g.ToCapture(Point{1.5, -0.2})
	require.InDelta(t, 2560, p.X, 0.001)
	require.InDelta(t, 0, p.Y, 0.001)
}

func TestGeometryFromJSON(t *testing.T) {
	var g DisplayGeometry
	require.NoError(t, json.Unmarshal([]byte(`{"capture":{"x":0,"y":0,"width":1920,"height":1080},"video":{"x":0,"y":0,"width":1280,"height":720}}`), &g))
	require.InDelta(t, 1920, g.Capture.Width, math.SmallestNonzeroFloat64)
}
