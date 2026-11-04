import type { TFunction } from 'i18next'

// Labels for the decision events recorded by the backend request policy state
// (reasons, sources and health), the session behaviors, and the auto-disable
// sources reported by the channel operations summary.
export function policyLabel(t: TFunction, value: string): string {
  switch (value) {
    case 'channel_failover':
      return t('Primary and backup routing')
    case 'primary_ready':
      return t('Primary capacity is available')
    case 'primary_rotation_wait':
      return t('Primary accounts are resting; wait for recovery')
    case 'primary_unknown':
      return t('Primary capacity is unknown; try the primary')
    case 'primary_busy':
    case 'primary_empty':
    case 'primary_recovering':
      return t('No eligible backup; try the primary')
    case 'backup_empty':
    case 'primary_capacity_exhausted':
      return t('Primary has no available accounts')
    case 'backup_busy':
      return t('Primary is busy; use the backup')
    case 'backup_recovering':
      return t('Waiting for primary recovery confirmation')
    case 'backup_ready':
    case 'backup_unknown':
    case 'backup_after_primary_failure':
    case 'primary_retryable_error':
      return t('Primary attempt failed; try the backup')
    case 'primary_backup_exhausted':
      return t('Primary and backup attempts exhausted')
    case 'response_started':
      return t('Response already started; switching stopped')
    case 'request_cancelled':
      return t('Request cancelled')
    case 'upstream_failure':
      return t('Upstream request failed')
    case 'request_failed':
      return t('Request failed')
    case 'task_failed':
      return t('Task failed')
    case 'strict_session':
      return t('Session behavior prevents switching channels')
    case 'attempt_budget_exhausted':
      return t('Total attempt budget exhausted')
    case 'channel_error':
      return t('Channel error')
    case 'unrecognized_status':
      return t('Unrecognized status code')
    case 'task_accepted':
      return t('The task was accepted; submission cannot be replayed')
    case 'media_generation_no_retry':
      return t('Image and video generation is not retried')
    case 'pinned_channel':
      return t('Fixed channel constraint')
    case 'local_rejection':
      return t('Request rejected locally')
    case 'non_retryable_error':
    case 'system_retry_exclusion':
      return t('System retry exclusion')
    case 'status_not_retryable':
      return t('Status is outside the retry rules')
    case 'retry_status_matched':
      return t('Status matches the retry rules')
    case 'session_rule_matched':
      return t('Session rule matched')
    case 'channel_selected':
      return t('Channel selected')
    case 'request_completed':
      return t('Request completed')
    case 'stream_not_successful':
      return t('Stream did not complete successfully')
    case 'channel':
      return t('Channel')
    case 'global':
      return t('Global default')
    case 'global_and_channel':
      return t('Global and channel settings')
    case 'session_rule':
      return t('Session rule')
    case 'system':
      return t('System constraint')
    case 'request_type':
      return t('Request type')
    case 'routing':
      return t('Channel selection')
    case 'upstream':
      return t('Upstream response')
    case 'transport':
      return t('Upstream connection')
    case 'local':
      return t('Local check')
    case 'channel_constraint':
      return t('Fixed channel constraint')
    case 'unchanged':
      return t('Unchanged')
    case 'channel_disable_requested':
      return t('Channel disable requested')
    case 'key_disable_requested':
      return t('Current key disable requested')
    case 'off':
    case '':
      return t('Do not keep sessions')
    case 'prefer':
      return t('Prefer the original channel, allow switching')
    case 'strict':
      return t('Require the original channel')
    default:
      return value
  }
}
