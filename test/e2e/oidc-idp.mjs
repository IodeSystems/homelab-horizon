// A minimal OpenID Connect provider, for testing hz's relying-party side.
//
// Deliberately dependency-free: node's crypto can generate an RSA key, export
// it as a JWK, and sign an RS256 JWT, which is everything a provider needs to
// be real from hz's point of view. Discovery, the JWKS fetch, the signature,
// the issuer/audience/expiry checks and PKCE are all genuine — the only thing
// stubbed is the part where a human types a password, which hz never sees.
//
// Behaviour is steered per-request by query parameters on /authorize so one
// provider can play both the cooperative and the hostile case:
//   ?sub=      subject to issue           (default sub-carl)
//   ?username= preferred_username claim   (default carl)
//   ?groups=   comma-separated groups     (default admins)
//   ?nonce_override=  sign a different nonce than requested
//
// Usage: node oidc-idp.mjs <port> <issuer>
import { createServer } from "node:http";
import { generateKeyPairSync, createSign, randomUUID, createHash } from "node:crypto";

const PORT = Number(process.argv[2] || 9000);
const ISSUER = process.argv[3] || `http://127.0.0.1:${PORT}`;
const KID = "e2e-key-1";

const { publicKey, privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
const jwk = { ...publicKey.export({ format: "jwk" }), kid: KID, alg: "RS256", use: "sig" };

const b64url = (buf) => Buffer.from(buf).toString("base64url");

function signJWT(payload) {
  const header = b64url(JSON.stringify({ alg: "RS256", typ: "JWT", kid: KID }));
  const body = b64url(JSON.stringify(payload));
  const signer = createSign("RSA-SHA256");
  signer.update(`${header}.${body}`);
  return `${header}.${body}.${signer.sign(privateKey, "base64url")}`;
}

// Authorization codes, issued by /authorize and redeemed once at /token.
const codes = new Map();

const json = (res, obj, status = 200) => {
  res.writeHead(status, { "content-type": "application/json" });
  res.end(JSON.stringify(obj));
};

const server = createServer((req, res) => {
  const url = new URL(req.url, ISSUER);

  if (url.pathname === "/.well-known/openid-configuration") {
    return json(res, {
      issuer: ISSUER,
      authorization_endpoint: `${ISSUER}/authorize`,
      token_endpoint: `${ISSUER}/token`,
      jwks_uri: `${ISSUER}/jwks`,
      response_types_supported: ["code"],
      subject_types_supported: ["public"],
      id_token_signing_alg_values_supported: ["RS256"],
      scopes_supported: ["openid", "profile", "email", "groups"],
      code_challenge_methods_supported: ["S256"],
    });
  }

  if (url.pathname === "/jwks") {
    return json(res, { keys: [jwk] });
  }

  // No login page: a real one would ask for a password hz never sees, so it
  // would add nothing to a test of hz.
  if (url.pathname === "/authorize") {
    const q = url.searchParams;
    const code = randomUUID();
    codes.set(code, {
      nonce: q.get("nonce_override") || q.get("nonce"),
      challenge: q.get("code_challenge"),
      sub: q.get("sub") || "sub-carl",
      username: q.get("username") || "carl",
      groups: (q.get("groups") ?? "admins").split(",").filter(Boolean),
      email: q.get("email") || "carl@e2e.test",
    });
    const redirect = new URL(q.get("redirect_uri"));
    redirect.searchParams.set("code", code);
    redirect.searchParams.set("state", q.get("state") ?? "");
    res.writeHead(302, { location: redirect.toString() });
    return res.end();
  }

  if (url.pathname === "/token" && req.method === "POST") {
    let body = "";
    req.on("data", (chunk) => (body += chunk));
    req.on("end", () => {
      const form = new URLSearchParams(body);
      const entry = codes.get(form.get("code"));
      // Single use, like a real provider: a code that can be redeemed twice
      // is a code worth stealing.
      codes.delete(form.get("code"));
      if (!entry) return json(res, { error: "invalid_grant" }, 400);

      // PKCE, verified rather than accepted: without this the fixture would
      // pass even if hz sent no verifier at all.
      const verifier = form.get("code_verifier") || "";
      const computed = createHash("sha256").update(verifier).digest("base64url");
      if (!entry.challenge || computed !== entry.challenge) {
        return json(res, { error: "invalid_grant", error_description: "PKCE mismatch" }, 400);
      }

      const now = Math.floor(Date.now() / 1000);
      const idToken = signJWT({
        iss: ISSUER,
        aud: form.get("client_id") || "hz-e2e",
        sub: entry.sub,
        exp: now + 300,
        iat: now,
        nonce: entry.nonce,
        preferred_username: entry.username,
        email: entry.email,
        email_verified: true,
        groups: entry.groups,
      });
      return json(res, {
        access_token: "e2e-access-token",
        token_type: "Bearer",
        expires_in: 300,
        id_token: idToken,
      });
    });
    return undefined;
  }

  return json(res, { error: "not_found" }, 404);
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`idp: listening on ${ISSUER}`);
});
