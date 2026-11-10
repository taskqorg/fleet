"""Disposable signing fixtures; no real identity token is retained."""
import asyncio
import time
from types import SimpleNamespace
import httpx
import jwt
from cryptography.hazmat.primitives.asymmetric import rsa
import origin_guard as guard

key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
guard.keys = SimpleNamespace(get_signing_key_from_jwt=lambda _: SimpleNamespace(key=key.public_key()))
claims = dict(iss=guard.ISSUER, aud=guard.AUDIENCE, email='Owner@Example.invalid',
              sub='fixture', iat=int(time.time()), exp=int(time.time()) + 60)
def encode(value):
    return jwt.encode(value, key, algorithm='RS256')

async def main():
    async with httpx.AsyncClient(transport=httpx.ASGITransport(app=guard.app), base_url='http://fixture.invalid') as c:
        async def status(token):
            r = await c.get('/verify', headers={'cf-access-jwt-assertion': token})
            if r.status_code == 204:
                assert r.headers['authorization'] == 'Bearer ' + guard.BROWSER_SECRET
                assert r.headers['x-fleet-person'] == jwt.decode(token, options={'verify_signature': False})['email'].lower()
            else:
                assert 'authorization' not in r.headers and 'x-fleet-person' not in r.headers
            return r.status_code
        assert await status(encode(claims)) == 204
        # Any verified e-mail passes the guard; the coordinator maps it to a person or answers 403.
        assert await status(encode(claims | {'email':'alina@example.invalid'})) == 204
        r = await c.get('/verify', headers={'cf-access-jwt-assertion': encode(claims), 'X-Fleet-Person': 'forged@example.invalid'})
        assert r.headers['x-fleet-person'] == 'owner@example.invalid'
        for change in ({'iss':'wrong'}, {'aud':'wrong'}, {'email':''}, {'exp':1}, {'iat':int(time.time())+3600}):
            assert await status(encode(claims | change)) == 403
        for field in ('exp','iat','sub','email'):
            assert await status(encode({k:v for k,v in claims.items() if k != field})) == 403
        forged = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        for token in (jwt.encode(claims, forged, algorithm='RS256'), jwt.encode(claims, 'x'*40, algorithm='HS256'), 'malformed', 'x'*16385):
            assert await status(token) == 403
        for headers in ({}, {'X-Dashq-Subject':'forged'}, [('cf-access-jwt-assertion',encode(claims))]*2):
            assert (await c.get('/verify', headers=headers)).status_code == 403
        def unavailable(_):
            raise jwt.PyJWKClientConnectionError('fixture')
        guard.keys = SimpleNamespace(get_signing_key_from_jwt=unavailable)
        assert await status(encode(claims)) == 503
    print('PASS: positive fixture; issuer/audience/e-mail header/time/required claims/signature/algorithm/header/key outage negatives')
asyncio.run(main())
