package main

// Host routes: every /api/hosts/ route is registered one way, hostRoute on its generated descriptor
// (contracts/host-protocol.schema.json routes, contract.Route<Name>). The descriptor names the request and reply types,
// whether the route runs without a surrounding transaction (long), and how its body is read (plain or checked).

import "fleet/contract"

// hostRoute registers route r: the body decodes into Req (plain: decoded only; checked: required keys at every depth,
// Validate, and unknown keys refused when Req is closed), the handler answers Rep. A host request always names its host
// (authorize), checked again here.
func hostRoute[Req, Rep any, P interface {
	*Req
	Validate() error
}](r contract.HostRoute[Req, Rep], h func(q *Request, b *Req) (Rep, error)) {
	hostRouteObserved[Req, Rep, P](r, nil, h)
}

// hostRouteObserved: hostRoute whose observe, when set, runs around the whole request, a body refused by its decoding
// included (the poll records its health this way).
func hostRouteObserved[Req, Rep any, P interface {
	*Req
	Validate() error
}](r contract.HostRoute[Req, Rep], observe func(q *Request, run func() (any, error)) (any, error), h func(q *Request, b *Req) (Rep, error)) {
	register := route
	if r.Long {
		register = longRoute
	}
	_, closed := any(P(nil)).(interface{ Closed() bool })
	serve := func(q *Request) (any, error) {
		b := new(Req)
		if r.Plain {
			if err := decode(q.body, b); err != nil {
				return nil, err
			}
		} else {
			var err error
			if b, err = decodeValid[Req, P](q.body, closed); err != nil {
				return nil, err
			}
		}
		out, err := h(q, b)
		if err != nil {
			return nil, err
		}
		return out, nil
	}
	register("POST "+r.Path, func(q *Request) (any, error) {
		if q.host == nil {
			return nil, fail(403, "Host rejected")
		}
		if observe != nil {
			return observe(q, func() (any, error) { return serve(q) })
		}
		return serve(q)
	})
}

// replyOf: a handler that answers its route's reply as any (its value is already Rep, or a map in Rep's shape) as one
// answering Rep.
func replyOf[Req, Rep any](h func(q *Request, b *Req) (any, error)) func(q *Request, b *Req) (Rep, error) {
	return func(q *Request, b *Req) (Rep, error) {
		v, err := h(q, b)
		if r, ok := v.(Rep); ok || err != nil || v == nil {
			return r, err
		}
		return asT[Rep](v)
	}
}

// executionOwned: the request's execution (required) runs on the calling host.
func executionOwned[Req, Rep any](id func(b *Req) string, h func(q *Request, b *Req) (Rep, error)) func(q *Request, b *Req) (Rep, error) {
	return func(q *Request, b *Req) (Rep, error) {
		var zero Rep
		x := id(b)
		if x == "" {
			return zero, fail(400, "Expected executionId")
		}
		e, err := execGet(q.ctx(), q.app, x)
		if err != nil {
			return zero, err
		}
		if e.HostID != q.hostID() {
			return zero, fail(403, "Execution rejected")
		}
		return h(q, b)
	}
}
