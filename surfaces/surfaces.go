// Package surfaces maps a provider's wire format onto the canonical request model
// (canon.Request) and back. Adding support for an agent/provider API is a new
// Surface, not a new copy of the engine — the same stage pipeline runs for every
// surface.
package surfaces

import (
	"errors"

	"github.com/kagenti/lab-context-engineering/canon"
)

// ErrUnsupported is returned by a surface that cannot map its wire format to the
// canonical model yet. Callers MUST treat it as "forward the original request
// untouched" (fail-open), never as a hard failure.
var ErrUnsupported = errors.New("surfaces: wire format not supported for reduction")

// RenderToken is opaque per-request state a surface needs to reconstruct the wire
// request after the canonical one has been reduced. It is nil for the identity
// (Anthropic) surface.
type RenderToken any

// Surface is the wire ⇄ canonical seam.
type Surface interface {
	// Name identifies the wire format ("anthropic", "openai", "gemini").
	Name() string
	// ToInternal parses a wire request body into the canonical model plus a render
	// token. It returns ErrUnsupported when the format cannot be reduced.
	ToInternal(body []byte) (canon.Request, RenderToken, error)
	// Render serializes a (possibly reduced) canonical request back to the wire
	// format using the token from ToInternal.
	Render(req canon.Request, token RenderToken) ([]byte, error)
}

// Anthropic is the identity surface: the canonical model IS the Anthropic wire
// shape, so both directions are decode/encode of the same object.
type Anthropic struct{}

func (Anthropic) Name() string { return "anthropic" }

func (Anthropic) ToInternal(body []byte) (canon.Request, RenderToken, error) {
	req, err := canon.Decode(body)
	return req, nil, err
}

func (Anthropic) Render(req canon.Request, _ RenderToken) ([]byte, error) {
	return req.Encode()
}

// Gemini (generateContent) is not yet mapped to the canonical model. It returns
// ErrUnsupported so the proxy forwards Gemini traffic untransformed — correct and
// safe — until a faithful mapping lands.
type Gemini struct{}

func (Gemini) Name() string { return "gemini" }

func (Gemini) ToInternal(body []byte) (canon.Request, RenderToken, error) {
	return canon.Request{}, nil, ErrUnsupported
}

func (Gemini) Render(req canon.Request, _ RenderToken) ([]byte, error) {
	return req.Encode()
}
