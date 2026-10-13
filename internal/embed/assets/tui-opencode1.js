import { effect as _$effect } from "@opentui/solid";
import { insert as _$insert } from "@opentui/solid";
import { createTextNode as _$createTextNode } from "@opentui/solid";
import { insertNode as _$insertNode } from "@opentui/solid";
import { setProp as _$setProp } from "@opentui/solid";
import { createElement as _$createElement } from "@opentui/solid";
/** @jsxImportSource @opentui/solid */

export default {
  id: "jailoc",
  tui: api => {
    if (!process.env.JAILOC) return;
    const workspace = process.env.JAILOC_WORKSPACE || "unknown";
    api.slots.register({
      order: 150,
      slots: {
        sidebar_content() {
          const theme = api.theme.current;
          return (() => {
            var _el$ = _$createElement("box"),
              _el$2 = _$createElement("text"),
              _el$3 = _$createTextNode(`🔒 jailoc / `);
            _$insertNode(_el$, _el$2);
            _$setProp(_el$, "flexDirection", "column");
            _$insertNode(_el$2, _el$3);
            _$insert(_el$2, workspace, null);
            _$effect(_$p => _$setProp(_el$2, "color", theme.text.secondary, _$p));
            return _el$;
          })();
        }
      }
    });
  }
};