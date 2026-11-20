# Go API refresh sessions

Set AUTH_REFRESH_SESSIONS=true in a local or staging API to enable this contract. Existing clients can continue to use their existing signed bearer token contract during migration.

Send X-BakeU-Session: refresh on POST /api/auth/login or /api/auth/signup to receive a 15-minute access token and a 30-day rotating refresh token. The JSON fields are token, refreshToken and expiresIn. Native clients should keep the refresh token in OS secure storage and serialize refresh requests across requests and processes.

- POST /api/auth/refresh takes a JSON refreshToken and returns a new pair. Replace the stored token immediately. If a response is lost and the old token is submitted again, the server revokes the session and the client must sign in.
- POST /api/auth/logout takes a JSON refreshToken and revokes its session.
- POST /api/auth/logout-all takes a bearer token and revokes all MongoDB sessions for that user. Legacy stateless tokens expire on their original schedule.
- Requests using a session-bound access token check the session in MongoDB, so revocation also invalidates already-issued access tokens.

Start the Go service with the flag enabled before using the contract so it can create the TTL and user indexes on auth_sessions. Website cookie transport, CSRF protection and client rollout still need implementation. Keep the flag off in production until the client rollout is reviewed.
