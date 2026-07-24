import type { ConfigObject } from '../api';
import { AuthorityPill } from './AuthorityPill';

const humanise = (v: string) => v.replace(/_/g, ' ').replace(/^\w/, (c) => c.toUpperCase());

interface Props {
  items: ConfigObject[];
  onOpen: (cfgId: string) => void;
}

export function EstateTable({ items, onOpen }: Props) {
  return (
    <table className="estate">
      <caption className="visually-hidden">
        Governed artifacts, showing authority, role, lifecycle and version
      </caption>
      <thead>
        <tr>
          <th scope="col">Artifact</th>
          <th scope="col">Authority</th>
          <th scope="col">Role</th>
          <th scope="col">Lifecycle</th>
          <th scope="col">Version</th>
        </tr>
      </thead>
      <tbody>
        {items.map((o) => (
          <tr key={o.cfg_id}>
            <th scope="row" className="artifact">
              {/* A real button: keyboard-reachable and announced, unlike a clickable row. */}
              <button type="button" className="link artifact-name" onClick={() => onOpen(o.cfg_id)}>
                {o.artifact}
              </button>
              <span className="cfg-id">{o.cfg_id}</span>
            </th>
            <td>
              <AuthorityPill value={o.authority} />
            </td>
            <td>{humanise(o.role)}</td>
            <td>{humanise(o.lifecycle)}</td>
            <td className="version">{o.version}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
