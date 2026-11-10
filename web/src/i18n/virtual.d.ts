declare module "virtual:octopus-messages" {
  /** A message, or its plural forms keyed by CLDR category. */
  export type Message = string | Partial<Record<Intl.LDMLPluralRule, string>>;
  export const locale: string;
  export const sourceLocale: string;
  export const locales: string[];
  export const messages: Record<string, Message>;
}
