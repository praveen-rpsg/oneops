import { useCallback, useEffect, useState } from 'react';
import Alert from '@cloudscape-design/components/alert';
import Box from '@cloudscape-design/components/box';
import Button from '@cloudscape-design/components/button';
import Form from '@cloudscape-design/components/form';
import KeyValuePairs from '@cloudscape-design/components/key-value-pairs';
import Modal from '@cloudscape-design/components/modal';
import SpaceBetween from '@cloudscape-design/components/space-between';
import StatusIndicator from '@cloudscape-design/components/status-indicator';
import { ApiError } from '../api';
import type { ProblemDetail } from '../api';
import { SEVERITY_TYPE, humanise } from '../incidentPresentation';
import { deleteIOC, getIOC, iocDescriptionError, iocSourceError, patchIOC } from '../iocs';
import type { IOCDTO } from '../iocs';
import { IOCConfigFields } from './IOCForm';
import type { IOCConfigValues } from './IOCForm';
import { ErrorState } from './States';

/**
 * "Edit indicator" (E-SEC-UI.2): the four fields the create modal collects
 * beyond indicator_type/indicator_value, pre-filled from the entry just
 * read, PATCHed with the `row_version` that same read carried.
 * indicator_type/indicator_value are shown read-only above the form —
 * `patchIOCRequest` cannot change them (see `iocs.ts`'s `IOCPatchInput` doc
 * comment: delete and recreate instead).
 */
function EditIOCModal({
  ioc,
  onClose,
  onSaved,
  onConflict,
}: {
  ioc: IOCDTO;
  onClose: () => void;
  onSaved: () => void;
  onConflict: () => void;
}) {
  const [config, setConfig] = useState<IOCConfigValues>({
    severity: ioc.severity,
    enabled: ioc.enabled,
    description: ioc.description,
    source: ioc.source,
  });
  const [busy, setBusy] = useState(false);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);

  const descriptionError = iocDescriptionError(config.description);
  const sourceError = iocSourceError(config.source);
  const canSubmit = !descriptionError && !sourceError && !busy;

  const submit = useCallback(async () => {
    if (!canSubmit) return;
    setBusy(true);
    setProblem(null);
    try {
      await patchIOC(
        ioc.ioc_id,
        { severity: config.severity, enabled: config.enabled, description: config.description, source: config.source },
        ioc.row_version,
      );
      onSaved();
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        onConflict();
      } else {
        setProblem(err instanceof ApiError ? err.problem : { title: 'Update failed', status: 0, detail: String(err) });
      }
    } finally {
      setBusy(false);
    }
  }, [canSubmit, ioc, config, onSaved, onConflict]);

  return (
    <Modal
      visible
      header={`Edit ${ioc.indicator_value}`}
      onDismiss={() => {
        if (!busy) onClose();
      }}
      closeAriaLabel="Dismiss"
      footer={
        <Box float="right">
          <SpaceBetween direction="horizontal" size="xs">
            <Button variant="link" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" loading={busy} disabled={!canSubmit} onClick={() => void submit()}>
              Save
            </Button>
          </SpaceBetween>
        </Box>
      }
    >
      <Form>
        <SpaceBetween size="m">
          {problem && (
            <div role="alert">
              <Alert type="error" header="Could not update the indicator">
                {problem.detail ?? `The server returned ${problem.status}.`}
              </Alert>
            </div>
          )}
          <Box color="text-body-secondary" fontSize="body-s">
            {humanise(ioc.indicator_type)} {ioc.indicator_value} — fixed at creation, not editable here.
          </Box>
          <IOCConfigFields
            values={config}
            onChange={setConfig}
            disabled={busy}
            errors={{ description: descriptionError, source: sourceError }}
          />
        </SpaceBetween>
      </Form>
    </Modal>
  );
}

/**
 * The `SplitPanel` content for one watchlist entry, reusing `GET
 * /v1/admin/iocs/{id}` unchanged — the same load/error structure
 * `AlertRuleDetailPanel`/`SecurityRuleDetailPanel` establish. Edit,
 * Enable/Disable and Delete all follow the same optimistic-lock/409/refetch
 * discipline ADR-ACT-001/ADR-ACT-002 set — except Delete, which the
 * confirmed contract carries no `row_version` for at all (see
 * `deleteIOC`'s doc comment). `onChanged` refreshes the host board after
 * edit/enable-disable; `onDeleted` additionally tells the host to close this
 * panel, since its subject no longer exists.
 */
export function IOCDetailPanel({
  iocId,
  onChanged,
  onDeleted,
}: {
  iocId: string;
  onChanged?: () => void;
  onDeleted?: () => void;
}) {
  const [ioc, setIoc] = useState<IOCDTO | null>(null);
  const [loading, setLoading] = useState(true);
  const [problem, setProblem] = useState<ProblemDetail | null>(null);
  const [retries, setRetries] = useState(0);

  const [busy, setBusy] = useState(false);
  const [actionProblem, setActionProblem] = useState<ProblemDetail | null>(null);
  const [conflictNotice, setConflictNotice] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);

  const refetch = useCallback(() => setRetries((n) => n + 1), []);

  useEffect(() => {
    const ctrl = new AbortController();
    setLoading(true);
    setProblem(null);

    getIOC(iocId, ctrl.signal)
      .then(setIoc)
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setProblem(err instanceof ApiError ? err.problem : { title: 'Could not load indicator', status: 0, detail: String(err) });
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setLoading(false);
      });

    return () => ctrl.abort();
  }, [iocId, retries]);

  /** Runs after an edit/enable-disable mutation: success and 409 both refetch; only other errors leave state as-is. */
  const afterMutation = useCallback(
    (outcome: 'ok' | 'conflict') => {
      setConflictNotice(outcome === 'conflict');
      refetch();
      onChanged?.();
    },
    [refetch, onChanged],
  );

  const toggleEnabled = useCallback(async () => {
    if (!ioc) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await patchIOC(ioc.ioc_id, { enabled: !ioc.enabled }, ioc.row_version);
      afterMutation('ok');
    } catch (err) {
      if (err instanceof ApiError && err.problem.status === 409) {
        afterMutation('conflict');
      } else {
        setActionProblem(err instanceof ApiError ? err.problem : { title: 'Update failed', status: 0, detail: String(err) });
      }
    } finally {
      setBusy(false);
    }
  }, [ioc, afterMutation]);

  const runDelete = useCallback(async () => {
    if (!ioc) return;
    setBusy(true);
    setActionProblem(null);
    try {
      await deleteIOC(ioc.ioc_id);
      setConfirmDelete(false);
      onDeleted?.();
    } catch (err) {
      setConfirmDelete(false);
      setActionProblem(err instanceof ApiError ? err.problem : { title: 'Delete failed', status: 0, detail: String(err) });
    } finally {
      setBusy(false);
    }
  }, [ioc, onDeleted]);

  if (problem) return <ErrorState problem={problem} onRetry={refetch} />;

  if (loading && !ioc) {
    return (
      <Box color="text-body-secondary" padding="l">
        Loading indicator…
      </Box>
    );
  }

  if (!ioc) return null;

  return (
    <SpaceBetween size="l">
      {conflictNotice && (
        <div role="alert">
          <Alert type="warning" header="Changed since you loaded it" dismissible onDismiss={() => setConflictNotice(false)}>
            This indicator was modified concurrently. It has been reloaded to the current state below — review it and
            try again.
          </Alert>
        </div>
      )}

      {actionProblem && (
        <div role="alert">
          <Alert type="error" header="Action failed" dismissible onDismiss={() => setActionProblem(null)}>
            {actionProblem.detail ?? `The server returned ${actionProblem.status}.`}
          </Alert>
        </div>
      )}

      <SpaceBetween direction="horizontal" size="xs">
        <Button disabled={busy} onClick={() => setEditOpen(true)}>
          Edit
        </Button>
        <Button disabled={busy} loading={busy} onClick={() => void toggleEnabled()}>
          {ioc.enabled ? 'Disable' : 'Enable'}
        </Button>
        <Button disabled={busy} onClick={() => setConfirmDelete(true)}>
          Delete
        </Button>
      </SpaceBetween>

      <KeyValuePairs
        columns={2}
        items={[
          { label: 'Indicator type', value: humanise(ioc.indicator_type) },
          { label: 'Indicator value', value: ioc.indicator_value },
          {
            label: 'Severity',
            value: <StatusIndicator type={SEVERITY_TYPE[ioc.severity]}>{humanise(ioc.severity)}</StatusIndicator>,
          },
          {
            label: 'Enabled',
            value: <StatusIndicator type={ioc.enabled ? 'success' : 'stopped'}>{ioc.enabled ? 'Enabled' : 'Disabled'}</StatusIndicator>,
          },
          { label: 'Source', value: ioc.source || <Box color="text-body-secondary">—</Box> },
          { label: 'Description', value: ioc.description || <Box color="text-body-secondary">—</Box> },
          { label: 'Created', value: new Date(ioc.created_at).toLocaleString() },
          { label: 'Updated', value: new Date(ioc.updated_at).toLocaleString() },
        ]}
      />

      {editOpen && (
        <EditIOCModal
          ioc={ioc}
          onClose={() => setEditOpen(false)}
          onSaved={() => {
            setEditOpen(false);
            afterMutation('ok');
          }}
          onConflict={() => {
            setEditOpen(false);
            afterMutation('conflict');
          }}
        />
      )}

      {confirmDelete && (
        <Modal
          visible
          header={`Delete ${ioc.indicator_value}?`}
          onDismiss={() => {
            if (!busy) setConfirmDelete(false);
          }}
          closeAriaLabel="Dismiss"
          footer={
            <Box float="right">
              <SpaceBetween direction="horizontal" size="xs">
                <Button variant="link" onClick={() => setConfirmDelete(false)} disabled={busy}>
                  Cancel
                </Button>
                <Button variant="primary" loading={busy} onClick={() => void runDelete()}>
                  Delete
                </Button>
              </SpaceBetween>
            </Box>
          }
        >
          <Box>
            This permanently removes the watchlist entry — it will no longer be matched, and this cannot be undone.
            Unlike Edit, there is no optimistic-lock check on delete (the endpoint takes no row_version — see
            ADR-HARD-003).
          </Box>
        </Modal>
      )}
    </SpaceBetween>
  );
}
