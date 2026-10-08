# Media (internal/media): originals under originals/ stay private, the web
# sizes under public/ are read straight from S3 at MEDIA_PUBLIC_URL.

resource "aws_s3_bucket" "media" {
  bucket_prefix = "${local.name}-media-"
}

resource "aws_s3_bucket_ownership_controls" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

# ACLs stay blocked; only the bucket policy below may make objects public.
resource "aws_s3_bucket_public_access_block" "media" {
  bucket = aws_s3_bucket.media.id

  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = false
  restrict_public_buckets = false
}

resource "aws_s3_bucket_policy" "media_public_sizes" {
  bucket = aws_s3_bucket.media.id
  policy = data.aws_iam_policy_document.media_public_sizes.json

  depends_on = [aws_s3_bucket_public_access_block.media]
}

data "aws_iam_policy_document" "media_public_sizes" {
  statement {
    sid       = "PublicWebSizes"
    actions   = ["s3:GetObject"]
    resources = ["${aws_s3_bucket.media.arn}/public/*"]

    principals {
      type        = "*"
      identifiers = ["*"]
    }
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "media" {
  bucket = aws_s3_bucket.media.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Nightly PostgreSQL dumps (deploy/backup.sh). Private, unversioned, and
# expired after backup_retention_days.

resource "aws_s3_bucket" "backups" {
  bucket_prefix = "${local.name}-backups-"
}

resource "aws_s3_bucket_ownership_controls" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    object_ownership = "BucketOwnerEnforced"
  }
}

resource "aws_s3_bucket_public_access_block" "backups" {
  bucket = aws_s3_bucket.backups.id

  block_public_acls       = true
  ignore_public_acls      = true
  block_public_policy     = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_server_side_encryption_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

# Stated, not defaulted: a dump is written once, and versions would only keep
# expired dumps alive.
resource "aws_s3_bucket_versioning" "backups" {
  bucket = aws_s3_bucket.backups.id

  versioning_configuration {
    status = "Disabled"
  }
}

resource "aws_s3_bucket_lifecycle_configuration" "backups" {
  bucket = aws_s3_bucket.backups.id

  rule {
    id     = "expire-dumps"
    status = "Enabled"

    filter {}

    expiration {
      days = var.backup_retention_days
    }

    abort_incomplete_multipart_upload {
      days_after_initiation = 1
    }
  }
}
