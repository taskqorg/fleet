"""Fleet-only variant of the reviewed host Access verifier."""
import os
import jwt
from fastapi import FastAPI, Request, Response

ISSUER = 'https://graduz.cloudflareaccess.com'
AUDIENCE = '352f4c75fdee7abac35e5ee8739ab622de4a368725eedac025c33b4e32df1ab0'
OWNER = os.environ['FLEET_ACCESS_OWNER']
BROWSER_SECRET = os.environ['FLEET_BROWSER_SECRET']
if not BROWSER_SECRET:
    raise RuntimeError('Missing browser credential')
keys = jwt.PyJWKClient(ISSUER + '/cdn-cgi/access/certs', timeout=5)
app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)


def verify(token, key):
    claims = jwt.decode(token, key, algorithms=['RS256'], audience=AUDIENCE,
                        issuer=ISSUER, options={'require': ['exp', 'iat', 'sub', 'email']})
    if claims['email'] != OWNER:
        raise jwt.InvalidTokenError('Identity denied')
    return claims


@app.get('/verify')
def check(request: Request):
    tokens = request.headers.getlist('cf-access-jwt-assertion')
    if len(tokens) != 1 or not tokens[0] or len(tokens[0]) > 16384:
        return Response(status_code=403)
    try:
        verify(tokens[0], keys.get_signing_key_from_jwt(tokens[0]).key)
    except jwt.PyJWKClientConnectionError:
        return Response(status_code=503)
    except jwt.PyJWTError:
        return Response(status_code=403)
    return Response(status_code=204, headers={'Authorization': 'Bearer ' + BROWSER_SECRET})
