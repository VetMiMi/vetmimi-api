-- CreateUser and ReplaceUserCredentials store the step of the code typed at
-- enrolment as already accepted, so that code can never also sign in.
-- name: CreateUser :one
INSERT INTO users (email, display_name, password_hash, roles, is_practitioner, totp_secret_enc, totp_last_step)
VALUES (@email, @display_name, @password_hash, @roles, @is_practitioner, @totp_secret_enc, @totp_last_step::bigint)
RETURNING id;

-- ReplaceUserCredentials re-enrols an existing user. It never clears
-- is_practitioner: re-running create-user to reset Daw Mi's password without
-- --practitioner must not leave the practice without its practitioner.
-- name: ReplaceUserCredentials :one
UPDATE users
SET display_name = @display_name,
    password_hash = @password_hash,
    roles = @roles,
    is_practitioner = is_practitioner OR @is_practitioner,
    totp_secret_enc = @totp_secret_enc,
    totp_last_step = @totp_last_step::bigint,
    updated_at = now()
WHERE email = @email
RETURNING id;

-- ClaimTOTPStep is the replay guard: a step is accepted once, and only if it
-- is later than the last one accepted, so two concurrent sign-ins with one
-- code cannot both succeed.
-- name: ClaimTOTPStep :execrows
UPDATE users
SET totp_last_step = @step::bigint
WHERE id = @id AND (totp_last_step IS NULL OR totp_last_step < @step);

-- name: GetUserForSignIn :one
SELECT id, email, display_name, password_hash, roles, is_practitioner, totp_secret_enc
FROM users
WHERE email = @email;

-- RecordSignIn succeeds only while the user is enabled and still has the
-- password hash sign-in verified. Its row lock orders it against a
-- concurrent create-user, whose session delete then sees this sign-in's
-- session, or whose new hash makes this update miss.
-- name: RecordSignIn :execrows
UPDATE users
SET last_sign_in_at = @now::timestamptz
WHERE id = @id AND disabled_at IS NULL AND password_hash = @password_hash;
