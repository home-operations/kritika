// The highlighted tokens of whatever a component shows, loaded when it is
// wanted and dropped when it shows something else.
import { untrack } from 'svelte';

// Highlighted keeps the tokens load gives for what of names, while on
// says they are wanted. value is undefined until they have loaded, when
// load gives none (no language for it), and once of names something else.
export class Highlighted<K, T> {
  // Raw: what was highlighted is compared by identity, which a state
  // proxy would hide.
  #got = $state.raw<{ of: K; tokens: T } | undefined>(undefined);
  readonly #of: () => K;

  constructor(of: () => K, load: (of: K) => Promise<T | undefined>, on: () => boolean = () => true) {
    this.#of = of;
    $effect(() => {
      const what = of();
      if (!on()) return;
      void untrack(() => load(what)).then((tokens) => {
        if (tokens && what === of()) this.#got = { of: what, tokens };
      });
    });
  }

  get value(): T | undefined {
    return this.#got?.of === this.#of() ? this.#got.tokens : undefined;
  }
}
