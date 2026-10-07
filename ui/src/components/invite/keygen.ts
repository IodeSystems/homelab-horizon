/**
 * The VPN invite's key generation — IN THE BROWSER (N4i).
 *
 * The device makes its own WireGuard keypair with WebCrypto X25519 and sends
 * hz only the public half. hz returns a config with
 * INVITE_PRIVATE_KEY_PLACEHOLDER where the private key goes; `fillConfig`
 * puts the device's key there, locally. The private key never leaves this
 * page.
 *
 * No dependency: WebCrypto X25519 is in current Chrome, Firefox and Safari.
 * A browser without it is told so (`x25519Unavailable`) — there is no
 * server-generated fallback, on purpose: that fallback is the path the server
 * held both halves on (CLAUDE.md #4).
 *
 * Pure apart from `crypto.subtle`, so `keygen.selftest.ts` runs it in Node.
 */

/** Must equal apitypes.InvitePrivateKeyPlaceholder (internal/apitypes/types.go). */
export const INVITE_PRIVATE_KEY_PLACEHOLDER = "<PRIVATE_KEY_FROM_THIS_DEVICE>";

export interface WGKeyPair {
  /** base64 of the raw 32-byte X25519 private key — WireGuard's format. */
  privateKey: string;
  /** base64 of the raw 32-byte X25519 public key. */
  publicKey: string;
}

function bytesToB64(b: Uint8Array): string {
  let s = "";
  for (const x of b) s += String.fromCharCode(x);
  return btoa(s);
}

/** JWK `d` is base64url without padding; WireGuard wants standard base64. */
export function b64urlToB64(s: string): string {
  const t = s.replace(/-/g, "+").replace(/_/g, "/");
  return t + "=".repeat((4 - (t.length % 4)) % 4);
}

/** Why X25519 cannot be used here, or "" when it can. */
export async function x25519Unavailable(subtle: SubtleCrypto | null | undefined = globalThis.crypto?.subtle): Promise<string> {
  if (!subtle) {
    return "This browser has no WebCrypto here (it needs a secure https:// page). Open the link in a current browser.";
  }
  try {
    await subtle.generateKey({ name: "X25519" }, true, ["deriveBits"]);
    return "";
  } catch {
    return "This browser cannot generate an X25519 key (WebCrypto X25519). Use a current Chrome, Firefox or Safari.";
  }
}

/** A fresh WireGuard keypair, made on this device. */
export async function generateWGKeyPair(subtle: SubtleCrypto = globalThis.crypto.subtle): Promise<WGKeyPair> {
  const pair = (await subtle.generateKey({ name: "X25519" }, true, ["deriveBits"])) as CryptoKeyPair;
  const jwk = await subtle.exportKey("jwk", pair.privateKey);
  if (!jwk.d) throw new Error("the browser exported no private key");
  const pub = new Uint8Array(await subtle.exportKey("raw", pair.publicKey));
  return { privateKey: b64urlToB64(jwk.d), publicKey: bytesToB64(pub) };
}

/** The config hz returned, with this device's private key in place. */
export function fillConfig(config: string, privateKey: string): string {
  const n = config.split(INVITE_PRIVATE_KEY_PLACEHOLDER).length - 1;
  if (n !== 1) {
    throw new Error(`the config from hz has ${n} private-key placeholders, expected exactly 1`);
  }
  return config.replace(INVITE_PRIVATE_KEY_PLACEHOLDER, privateKey);
}

/**
 * The body the page POSTs to /api/v1/vpn/invite/redeem: the token, the
 * PUBLIC key, the name. Built here, from the pair, so the selftest pins that
 * the private key is not in it and the public key is the pair's own.
 */
export function redeemRequest(token: string, pair: WGKeyPair, name: string): { token: string; publicKey: string; name: string } {
  return { token, publicKey: pair.publicKey, name: cleanDeviceName(name) };
}

/** A device name as wg0.conf and the peer list take it. */
export function cleanDeviceName(s: string): string {
  return s.trim().replace(/\s+/g, "-");
}
