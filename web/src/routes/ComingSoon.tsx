import Container from '@cloudscape-design/components/container';
import Header from '@cloudscape-design/components/header';
import Box from '@cloudscape-design/components/box';

/**
 * Placeholder for a section that has a nav entry but no screen yet
 * (E7-UI.1 NOC, E7-UI.2 Incidents). Gives the shell somewhere real to route to
 * without pretending the feature exists.
 */
export function ComingSoon({ title }: { title: string }) {
  return (
    <Container header={<Header variant="h1">{title}</Header>}>
      <Box color="text-body-secondary" textAlign="center" padding="l">
        Coming soon.
      </Box>
    </Container>
  );
}
