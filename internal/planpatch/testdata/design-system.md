# Recipe box — design system

## Tokens

| Token | Value |
|---|---|
| Colour, page | #FBF7F0 |
| Colour, text | #2B2118 |
| Colour, accent | #B4472B |
| Type, headings | Fraunces, 28 / 22 / 18 |
| Type, body | Inter, 16 on 24 |
| Spacing | 4, 8, 16, 24, 40 |
| Corner radius | 8 |

## Components

### RecipeCard

A card with the recipe's name in the heading type, the first two ingredient lines in the body type and a small clock with the cooking time. The whole card is the link to the recipe. On a hover the card lifts by 2 and takes the accent colour on its border.

### SearchBox

A single line with a magnifier on the left and the placeholder "What is in the fridge?". Pressing Enter runs the search. A clear button appears once there is text.

## Screens

### Home

The search box on top, then the recipe cards in two columns, sorted by name. On a phone the cards run in one column.

### Recipe

The recipe's name, the ingredient lines, then the steps. A Print button and a Share button sit under the name.

### Shared recipe

The Recipe screen with no buttons, shown after the reader signs in with an email address. A reader who is not signed in sees the sign-in form first and the recipe only after it.
