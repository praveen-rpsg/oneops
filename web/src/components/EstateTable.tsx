import type { ReactNode } from 'react';
import Table from '@cloudscape-design/components/table';
import type { TableProps } from '@cloudscape-design/components/table';
import Button from '@cloudscape-design/components/button';
import Box from '@cloudscape-design/components/box';
import type { ConfigObject } from '../api';
import { AuthorityPill } from './AuthorityPill';

const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

interface Props {
  items: ConfigObject[];
  onOpen: (cfgId: string) => void;
  loading: boolean;
  empty: ReactNode;
}

export function EstateTable({ items, onOpen, loading, empty }: Props) {
  const columns: TableProps.ColumnDefinition<ConfigObject>[] = [
    {
      id: 'artifact',
      header: 'Artifact',
      isRowHeader: true,
      cell: (o) => (
        <div>
          {/* A real button: keyboard-reachable and announced, unlike a clickable row. */}
          <Button variant="inline-link" onClick={() => onOpen(o.cfg_id)}>
            {o.artifact}
          </Button>
          <Box fontSize="body-s" color="text-body-secondary" fontWeight="normal">
            {o.cfg_id}
          </Box>
        </div>
      ),
    },
    { id: 'authority', header: 'Authority', cell: (o) => <AuthorityPill value={o.authority} /> },
    { id: 'role', header: 'Role', cell: (o) => humanise(o.role) },
    { id: 'lifecycle', header: 'Lifecycle', cell: (o) => humanise(o.lifecycle) },
    { id: 'version', header: 'Version', cell: (o) => <Box fontSize="body-s">{o.version}</Box> },
  ];

  return (
    <Table
      items={items}
      columnDefinitions={columns}
      trackBy="cfg_id"
      loading={loading}
      loadingText="Loading artifacts"
      empty={empty}
      variant="container"
      ariaLabels={{
        tableLabel: 'Governed artifacts, showing authority, role, lifecycle and version',
      }}
    />
  );
}
