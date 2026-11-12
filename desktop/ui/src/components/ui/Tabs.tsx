import { Icon, type IconName } from "../Icon";
import { cx } from "./styleHelpers";

export interface TabDef<K extends string> {
  key: K;
  label: string;
  icon?: IconName;
  count?: number;
}

export function Tabs<K extends string>({ tabs, value, onChange }: { tabs: TabDef<K>[]; value: K; onChange: (k: K) => void }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((tab) => (
        <button
          key={tab.key}
          type="button"
          role="tab"
          aria-selected={tab.key === value}
          className={cx("tabs__tab", tab.key === value && "tabs__tab--active")}
          onClick={() => onChange(tab.key)}
        >
          {tab.icon ? <Icon name={tab.icon} size={15} /> : null}
          {tab.label}
          {tab.count !== undefined ? <span className="tabs__count">({tab.count})</span> : null}
        </button>
      ))}
    </div>
  );
}
