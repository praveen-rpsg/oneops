import '@testing-library/jest-dom/vitest';
import { configure } from '@testing-library/react';

// testing-library's own default `asyncUtilTimeout` for `findBy*`/`waitFor` is
// 1000ms — fine on an idle machine, but a false-fail on a loaded one (other
// builds/servers competing for CPU: observed `collect`/`tests` phases north
// of 40s in that condition, well past the point a *correctly* awaited render
// still completes). Raising this is not a race-condition workaround — every
// `findBy*`/`waitFor` call in this suite already polls for the right thing
// (ADR-HARD-001); this only gives that poll enough wall-clock room to
// succeed under real CPU contention instead of asserting against the
// machine's scheduler. See ADR-HARD-001 §"Load-sensitivity".
configure({ asyncUtilTimeout: 5000 });
