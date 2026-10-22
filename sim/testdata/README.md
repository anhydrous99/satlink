# Test certificates

Fixtures for the simulator's TLS echo service. They protect nothing: the
server's private key is published here on purpose, so that anyone can run the
simulator. The CA's name is "iridium-sim test CA".

- `test-ca.pem`: the test CA, exported as `sim.SecureTestCAPEM()`. Trust it in
  tests, never in production.
- `secure.test.pem`: the certificate the service presents, for `secure.test`,
  issued by the test CA. Both are valid from 2026-10-19 03:17 UTC until 2126,
  so a machine whose clock is behind that fails the TLS tests.
- `secure.test.key`: its private key (P-256, PKCS#8). The SHA-256 of its
  public key is `sim.SecureTestKeySHA256()`:

  ```sh
  openssl x509 -in secure.test.pem -pubkey -noout | openssl pkey -pubin -outform der | openssl dgst -sha256
  ```

The CA's private key is deliberately not included: with it, anyone could issue
certificates for other names that every test client trusting the CA would
accept. Replacing these fixtures therefore means creating a new CA, and
updating `SecureTestKeySHA256` to the new server key.
