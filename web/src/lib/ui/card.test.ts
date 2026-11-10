import { describe, expect, it } from "vitest";

import { cardClasses, cardHTML, cardTags } from "./card";

describe("cardClasses", () => {
  it("writes card, then link, reveal, dashed, off and the extras", () => {
    expect(cardClasses()).toBe("card");
    expect(cardClasses({ reveal: "always" })).toBe("card");
    expect(
      cardClasses({ link: true, reveal: "hover", dashed: true, off: true, cls: "ev-env" }),
    ).toBe("card card-link card-reveal card-dashed card-off ev-env");
    expect(cardClasses({ target: true })).toBe("card card-target");
  });

  it("refuses a reveal that does not exist", () => {
    // @ts-expect-error: not a reveal
    expect(() => cardClasses({ reveal: "focus" })).toThrow(/no reveal "focus"/);
  });
});

describe("cardTags", () => {
  it("takes article and h3 by default", () => {
    expect(cardTags()).toEqual({ tag: "article", heading: "h3" });
    expect(cardTags("li", "h2")).toEqual({ tag: "li", heading: "h2" });
  });

  it("refuses a tag or a heading it does not draw", () => {
    // @ts-expect-error: not a tag
    expect(() => cardTags("span")).toThrow(/no tag "span"/);
    // @ts-expect-error: not a heading
    expect(() => cardTags("article", "h5")).toThrow(/no heading "h5"/);
  });
});

describe("cardHTML", () => {
  it("writes the head, the body and the foot in that order", () => {
    expect(
      cardHTML({
        title: "Sandbox <on demand>",
        headHTML: '<span class="badge">Off</span>',
        bodyHTML: "<p>Made per batch.</p>",
        footHTML: '<button class="btn">Edit</button>',
      }),
    ).toBe(
      '<article class="card"><div class="card-head"><h3>Sandbox &lt;on demand&gt;</h3>' +
        '<span class="badge">Off</span></div>' +
        '<div class="card-body"><p>Made per batch.</p></div>' +
        '<div class="card-foot"><button class="btn">Edit</button></div></article>',
    );
  });

  it("stretches the title's link over the card with an href", () => {
    const html = cardHTML({ title: "Product", href: "/settings/boards/?b=a&c=1" });
    expect(html).toBe(
      '<article class="card card-link"><div class="card-head"><h3>' +
        '<a class="card-cover" href="/settings/boards/?b=a&amp;c=1">Product</a></h3></div></article>',
    );
  });

  it("puts the poster first, before the head", () => {
    const html = cardHTML({ title: "EV-2", mediaHTML: '<img src="p.png" alt="">', heading: "h2" });
    expect(html).toBe(
      '<article class="card"><div class="card-media"><img src="p.png" alt=""></div>' +
        '<div class="card-head"><h2>EV-2</h2></div></article>',
    );
  });

  it("writes a head without a title when only the head markup is given", () => {
    expect(cardHTML({ headHTML: "<b>x</b>" })).toBe(
      '<article class="card"><div class="card-head"><b>x</b></div></article>',
    );
  });

  it("writes no head, body or foot that was not given", () => {
    expect(cardHTML({ tag: "li" })).toBe('<li class="card"></li>');
  });

  it("writes the variants, the id and the caller's attributes", () => {
    expect(
      cardHTML({
        tag: "div",
        id: "p-1",
        reveal: "hover",
        dashed: true,
        off: true,
        cls: "proj",
        attrs: { "data-id": "p<1>", hidden: true },
        bodyHTML: "b",
      }),
    ).toBe(
      '<div id="p-1" class="card card-reveal card-dashed card-off proj" data-id="p&lt;1&gt;" hidden>' +
        '<div class="card-body">b</div></div>',
    );
  });

  it("refuses a tag it does not draw", () => {
    // @ts-expect-error: not a tag
    expect(() => cardHTML({ tag: "span" })).toThrow(/no tag "span"/);
  });
});
