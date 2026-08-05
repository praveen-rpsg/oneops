import { useNavigate, useParams, useLocation } from 'react-router-dom';
import { ArtifactDetail } from '../components/ArtifactDetail';

interface NavigationState {
  /** Where "Back to estate" returns to — the list URL (with its filters) the user came from. */
  from?: string;
}

/**
 * Deep-linkable at `/artifacts/:id`. `from` is threaded through every subsequent
 * related-artifact navigation (not just the immediate previous one) so "Back to
 * estate" always returns to the original list view, no matter how many related
 * artifacts were opened in between — matching the single-level-back behaviour the
 * console has always had.
 */
export function ArtifactPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const location = useLocation();
  const from = (location.state as NavigationState | null)?.from ?? '/';

  if (!id) return null;

  return (
    <ArtifactDetail
      cfgId={id}
      onBack={() => navigate(from)}
      onOpen={(relatedId) => navigate(`/artifacts/${encodeURIComponent(relatedId)}`, { state: { from } })}
    />
  );
}
