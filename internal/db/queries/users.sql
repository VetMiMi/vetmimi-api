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

-- name: GetUserTOTPSecret :one
SELECT totp_secret_enc FROM users WHERE id = @id;

-- ClaimTOTPStep is the replay guard: a step is accepted once, and only if it
-- is later than the last one accepted, so two concurrent sign-ins with one
-- code cannot both succeed.
-- name: ClaimTOTPStep :execrows
UPDATE users
SET totp_last_step = @step::bigint
WHERE id = @id AND (totp_last_step IS NULL OR totp_last_step < @step);
