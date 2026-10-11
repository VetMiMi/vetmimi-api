#!/bin/sh
# Prints the demo's evidence, with the account id masked. Run it as the owner
# after an apply, with AWS_PROFILE set; deploy/terraform/README.md has the steps.
# Read-only, except that it forces one new api deployment to show a rolling
# deploy under load.
set -eu
cd "$(dirname "$0")"
export AWS_REGION="${AWS_REGION:-ap-southeast-2}"

name=$(terraform output -raw name)
cluster=$(terraform output -raw cluster)
db=$(terraform output -raw db_instance)
api_url=$(terraform output -raw api_url)
site_url=$(terraform output -raw site_url)
account=$(aws sts get-caller-identity --query Account --output text)

evidence() {
  echo "## ECS tasks"
  for service in api worker web; do
    tasks=$(aws ecs list-tasks --cluster "$cluster" --service-name "$service" --query 'taskArns' --output text)
    # $tasks unquoted: one argument per task.
    # shellcheck disable=SC2086
    aws ecs describe-tasks --cluster "$cluster" --tasks $tasks --output table \
      --query 'tasks[].[group, availabilityZone, lastStatus, healthStatus, cpu, memory]'
  done

  echo "## Target health"
  for service in api web; do
    tg=$(aws elbv2 describe-target-groups --names "$name-$service" --query 'TargetGroups[0].TargetGroupArn' --output text)
    echo "$service"
    aws elbv2 describe-target-health --target-group-arn "$tg" --output table \
      --query 'TargetHealthDescriptions[].[Target.AvailabilityZone, TargetHealth.State]'
  done

  echo "## RDS"
  aws rds describe-db-instances --db-instance-identifier "$db" --output table \
    --query 'DBInstances[0].{Class: DBInstanceClass, Engine: EngineVersion, MultiAZ: MultiAZ, Primary: AvailabilityZone, Secondary: SecondaryAvailabilityZone, Encrypted: StorageEncrypted, Status: DBInstanceStatus}'

  echo "## Autoscaling"
  aws application-autoscaling describe-scaling-policies --service-namespace ecs --output table \
    --query "ScalingPolicies[?PolicyName=='$name-api-cpu'].[PolicyName, TargetTrackingScalingPolicyConfiguration.TargetValue, TargetTrackingScalingPolicyConfiguration.PredefinedMetricSpecification.PredefinedMetricType]"
  aws application-autoscaling describe-scalable-targets --service-namespace ecs --output table \
    --resource-ids "service/$cluster/api" --query 'ScalableTargets[].[ResourceId, MinCapacity, MaxCapacity]'

  echo "## Alarms"
  aws cloudwatch describe-alarms --alarm-name-prefix "$name-" --output table \
    --query 'MetricAlarms[].[AlarmName, StateValue]'

  echo "## HTTPS via CloudFront"
  curl -sS -o /dev/null -w 'api  /readyz      HTTP/%{http_version} %{http_code} %{content_type}\n' "$api_url/readyz"
  curl -sS -o /dev/null -w 'web  /            HTTP/%{http_version} %{http_code} %{content_type}\n' "$site_url/"
  curl -sS -o /dev/null -w 'web  /api/health  HTTP/%{http_version} %{http_code} %{content_type}\n' "$site_url/api/health"

  echo "## Rolling deploy under load"
  log=$(mktemp)
  while :; do
    curl -s -o /dev/null -w '%{http_code}\n' --max-time 5 "$api_url/healthz" >>"$log" || true
    sleep 0.5
  done &
  loop=$!
  trap 'kill "$loop" 2>/dev/null; rm -f "$log"' EXIT
  started=$(date -u +%H:%M:%S)
  aws ecs update-service --cluster "$cluster" --service api --force-new-deployment --query 'service.serviceName' --output text >/dev/null
  aws ecs wait services-stable --cluster "$cluster" --services api
  kill "$loop"
  trap - EXIT
  echo "requests:  $(wc -l <"$log" | tr -d ' ')"
  echo "by status:"
  sort "$log" | uniq -c
  echo "window:    $started -> $(date -u +%H:%M:%S) UTC"
  rm -f "$log"

  echo "## ECS service events (api)"
  aws ecs describe-services --cluster "$cluster" --services api --output text \
    --query 'services[0].events[:12].[createdAt, message]'
}

evidence 2>&1 | sed "s/$account/<account>/g"
