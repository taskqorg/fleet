import type { SceneProps } from '../../../../scene-shell.tsx';
import {
  AcceptanceEvidence, Composer, GroupHeader, PulseLine, QuestionCard, SessionCard,
} from '../../../../../src/index.ts';
import { choices, rich, sparse } from './fixture.ts';

function AcceptCard(props: { item: typeof rich | typeof sparse }) {
  return (
    <QuestionCard code={props.item.code} question={props.item.question} choices={choices}
      context={<div class="type-sm"><AcceptanceEvidence q={props.item.evidence} /></div>} onAnswer={() => {}} />
  );
}

export default function Scene(props: SceneProps) {
  return (
    <div class="home" data-ui="home">
      <header class="home-head">
        <h2 class="home-hello">Good afternoon</h2>
        <PulseLine working={1} queued={0} done={17} countHref={() => "/live"} liveHref="/live" />
      </header>
      <Composer placeholder="Talk to anyone…" onSend={() => {}} />
      <section class="home-group" aria-label="Waiting for you">
        <GroupHeader icon="bell" tone="attention" label="Waiting for you" count={2} open onToggle={() => {}} />
        <div class="home-cards">
          <SessionCard name="Acceptance" project="fleet" avatar={{ letter: 'A', tone: 'attention' }}
            state="waiting" now="Asks you to accept a result." question={<AcceptCard item={props.path.endsWith("-sparse") ? sparse : rich} />} />
          <SessionCard name="Acceptance" project="fleet" avatar={{ letter: 'A', tone: 'attention' }}
            state="waiting" now="Asks you to accept a result." question={<AcceptCard item={props.path.endsWith("-sparse") ? rich : sparse} />} />
        </div>
      </section>
      <section class="home-group" aria-label="Working">
        <GroupHeader icon="activity" label="Working" count={1} open onToggle={() => {}} />
        <div class="home-cards">
          <SessionCard name="PM · Fleet" project="fleet" avatar={{ letter: 'P', tone: 'neutral' }}
            state="working" now="Checking queued work." />
        </div>
      </section>
    </div>
  );
}
