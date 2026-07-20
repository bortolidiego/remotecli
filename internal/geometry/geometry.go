// Package geometry normaliza coordenadas entre tela capturada e canvas remoto.
package geometry

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Rect define uma área de captura/injeção em pixels.
type Rect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Size retorna as dimensões deste retângulo.
func (r Rect) Size() Size {
	return Size{Width: r.Width, Height: r.Height}
}

// Size representa dimensões em pixels.
type Size struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// DisplayGeometry descreve a origem de captura e o canvas de vídeo.
type DisplayGeometry struct {
	Capture Rect `json:"capture"`
	Video   Rect `json:"video"`
	// Rotation em graus (0, 90, 180, 270). Por padrão 0.
	Rotation int `json:"rotation,omitempty"`
}

// IsZero indica se a geometria não foi configurada.
func (g DisplayGeometry) IsZero() bool {
	return g.Capture.Width == 0 || g.Capture.Height == 0 || g.Video.Width == 0 || g.Video.Height == 0
}

// Validate retorna erro se a geometria for inválida.
func (g DisplayGeometry) Validate() error {
	if g.IsZero() {
		return errors.New("geometria vazia")
	}
	if g.Capture.Width <= 0 || g.Capture.Height <= 0 {
		return errors.New("capture deve ter dimensões positivas")
	}
	if g.Video.Width <= 0 || g.Video.Height <= 0 {
		return errors.New("video deve ter dimensões positivas")
	}
	if g.Rotation != 0 && g.Rotation != 90 && g.Rotation != 180 && g.Rotation != 270 {
		return fmt.Errorf("rotação inválida: %d", g.Rotation)
	}
	return nil
}

// Point representa coordenadas normalizadas [0,1].
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Clip mantém o ponto dentro de [0,1].
func (p Point) Clip() Point {
	return Point{clip01(p.X), clip01(p.Y)}
}

// ToCapture converte coordenadas normalizadas do canvas de vídeo para pixels de captura.
func (g DisplayGeometry) ToCapture(p Point) Point {
	if g.IsZero() {
		return p
	}
	p = p.Clip()
	switch g.Rotation {
	case 90:
		// video (x,y) -> capture (1-y, x) mapeado pelas dimensões de captura.
		return Point{
			X: (1 - p.Y) * g.Capture.Width,
			Y: p.X * g.Capture.Height,
		}
	case 180:
		return Point{
			X: (1 - p.X) * g.Capture.Width,
			Y: (1 - p.Y) * g.Capture.Height,
		}
	case 270:
		return Point{
			X: p.Y * g.Capture.Width,
			Y: (1 - p.X) * g.Capture.Height,
		}
	default:
		return Point{
			X: p.X * g.Capture.Width,
			Y: p.Y * g.Capture.Height,
		}
	}
}

// MarshalJSON serializa DisplayGeometry.
func (g DisplayGeometry) MarshalJSON() ([]byte, error) {
	type alias DisplayGeometry
	return json.Marshal(alias(g))
}

// UnmarshalJSON desserializa DisplayGeometry.
func (g *DisplayGeometry) UnmarshalJSON(b []byte) error {
	type alias DisplayGeometry
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*g = DisplayGeometry(a)
	return nil
}

func clip01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
