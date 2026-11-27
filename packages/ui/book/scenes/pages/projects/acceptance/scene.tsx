import type { SceneProps } from '../../../../scene-shell.tsx';
import { NOW } from '../../../../clock.ts';
import {
  AcceptanceEvidence, Composer, DecisionBlock, DetailSection, GroupHeader, PulseLine, QuestionCard, SessionCard,
} from '../../../../../src/index.ts';
import { choices, rich, sparse } from './fixture.ts';

function AcceptCard(props: { item: typeof rich | typeof sparse }) {
  return (
    <QuestionCard code={props.item.code} question={props.item.question} choices={choices}
      context={<div class="type-sm"><AcceptanceEvidence q={props.item.evidence} /></div>} onAnswer={() => {}} />
  );
}

export default function Scene(props: SceneProps) {
  if (props.path.endsWith('/acceptance-task')) {
    return (
      <article class="detail" aria-label="Task details">
        <DetailSection title="Review screen">
          <DecisionBlock question={rich.question} askedBy="Fleet" addressedTo="you"
            since={NOW - 600000} now={NOW} typed={false}
            context={<AcceptanceEvidence q={rich.evidence} />}
            options={choices.map((choice, i) => ({ ...choice, kbd: String(i + 1) }))} />
        </DetailSection>
      </article>
    );
  }
  if (props.path.startsWith('/projects/')) {
    return (
      <article class="detail" aria-label="Fleet project overview">
        <DetailSection title="Waiting for you" count={2}>
          <div class="pw-cards">
            <AcceptCard item={rich} />
            <AcceptCard item={sparse} />
          </div>
        </DetailSection>
      </article>
    );
  }
  return null;
}
