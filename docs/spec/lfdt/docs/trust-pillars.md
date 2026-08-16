# Understanding Trust Pillars

In the digital world, establishing trust in any piece of information, whether it's a document, a transaction, or a digital credential, relies on three fundamental pillars: Integrity, Validity, and Authenticity.

## Integrity
- _What it means:_ Integrity means no changes. The data matches the original exactly. Integrity check means to verify if the information has not been changed, altered, or tampered with since it was originally created or issued. It's about making sure the data is exactly as the sender intended it to be, without any unauthorized modifications.
- _How it's checked:_ This is often verified using digital signatures or cryptographic hash functions. A unique "fingerprint" of the data is created when it's issued. If even a single character is changed, the fingerprint will no longer match, indicating tampering.
- _Example:_ Imagine a digital degree certificate. If someone tries to change your grade from a 'B' to an 'A' after it's been issued, the integrity check would fail because the digital signature on the certificate would no longer be valid for the altered content.

## Validity
- _What it means:_ Validity means still good. The information hasn't expired or been canceled. Validity check means to verify if the information is still current, relevant, and has not been revoked, expired, or superseded. It addresses whether the information is still "good" to use at the present time.
- _How it's checked:_ This typically involves checking against revocation lists (lists of certificates or credentials that have been cancelled), expiration dates, or updated versions of the information.
- _Example:_ A digital driver's license has an expiration date. Even if it's authentic and hasn't been tampered with, it becomes invalid after that date. Similarly, a digital certificate issued to a company might be revoked if the company goes out of business or its security is compromised.

## Authenticity
- _What it means:_ Authenticity means real source. The information comes from who it claims to come from. Authenticity check means to verify if the source of the information is genuinely who or what it claims to be. It's about trusting the origin of the data.
- _How it's checked:_ This is established by verifying the digital signature against the issuer's publicly listed digital certificate, public key, or other trusted identifiers. It also involves checking if the issuer is a recognized and authorized entity (e.g., a university accredited to issue degrees, a bank licensed to operate).
- _Example:_ If you receive a digital invoice, authenticity ensures that it truly came from the company you did business with, not a fraudster pretending to be them. You would check the company's digital signature and verify that the signing certificate was issued by a trusted Certificate Authority and that the company is a legitimate, registered business.
