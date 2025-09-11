package main

import (
	"image"
	"image/color"
	"image/png"
	"strconv"
	"strings"
	"bytes"
	"math/cmplx"
	"github.com/SimonWaldherr/Wigwam/api"
)

func Handle(req api.Request) api.Response {
	w := atoi(first(req.Query["w"]))
	h := atoi(first(req.Query["h"]))
	maxIter := atoi(first(req.Query["iter"]))
	zoom := atof(first(req.Query["zoom"]))
	cx := atof(first(req.Query["cx"]))
	cy := atof(first(req.Query["cy"]))
	if w<=0 { w=512 }; if h<=0 { h=512 }
	if maxIter<=0 { maxIter=300 }
	if zoom==0 { zoom=1 }

	img := image.NewRGBA(image.Rect(0,0,w,h))
	scale := 3.0/float64(min(w,h))/zoom
	center := complex(cx, cy)
	for py:=0; py<h; py++ {
		for px:=0; px<w; px++ {
			x := float64(px - w/2)
			y := float64(py - h/2)
			c := complex(x*scale, y*scale) + center
			z := complex(0,0)
			iter := 0
			for ; iter<maxIter && cmplx.Abs(z) <= 2; iter++ { z = z*z + c }
			img.Set(px,py, palette(iter, maxIter))
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return api.Response{Status:200, Header: map[string][]string{"Content-Type":{"image/png"}}, Body: buf.Bytes()}
}

func palette(i, max int) color.Color {
	if i == max { return color.Black }
	t := float64(i)/float64(max)
	return color.RGBA{R:uint8(9*(1-t)*t*t*t*255), G:uint8(15*(1-t)*(1-t)*t*t*255), B:uint8(8.5*(1-t)*(1-t)*(1-t)*t*255), A:255}
}

func first(v []string) string { if len(v)>0 { return v[0] }; return "" }
func atoi(s string) int { n,_ := strconv.Atoi(strings.TrimSpace(s)); return n }
func atof(s string) float64 { f,_ := strconv.ParseFloat(strings.TrimSpace(s),64); return f }
func min(a,b int) int { if a<b { return a }; return b }
