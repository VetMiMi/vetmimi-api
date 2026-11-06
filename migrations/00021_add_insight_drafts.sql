-- +goose Up
-- The three Insights the site used to hardcode, as drafts for Daw Mi to rewrite and publish.

WITH post AS (
    INSERT INTO posts (title, kind)
    SELECT 'What happens when we stop trying to make it perfect?', 'insight'
    WHERE NOT EXISTS (SELECT 1 FROM post_versions WHERE channel = 'website' AND slug = 'stop-trying-make-perfect')
    RETURNING id
)
INSERT INTO post_versions (post_id, channel, enabled, slug, title, excerpt, body)
SELECT id, 'website', true, 'stop-trying-make-perfect',
       '{"en": "What happens when we stop trying to make it perfect?", "my": "ပြီးပြည့်စုံအောင် ကြိုးစားနေတာကို ရပ်လိုက်တဲ့အခါ ဘာဖြစ်လာမလဲ။"}',
       '{"en": "A reflection on creating without needing an answer first. What opens up when the pressure to get it right is set aside for a while.", "my": "အဖြေကို အရင်မလိုအပ်ဘဲ ဖန်တီးခြင်းအကြောင်း ပြန်လည်ဆင်ခြင်မှုတစ်ခုပါ။ မှန်ကန်အောင် လုပ်ရမယ်ဆိုတဲ့ ဖိအားကို ခဏ ဘေးချထားလိုက်တဲ့အခါ ဘာတွေ ပွင့်လာမလဲ။"}',
       '{"en": "Some experiences arrive before we have the words for them. This reflection begins with noticing what changed when there was permission to make first and explain later.\n\nIn a session, someone once picked up a piece of paper and tore it. Not in frustration — in curiosity. They held the edges and looked at what the tear had made. It had not been planned. It had just happened.\n\nWhat followed was a conversation that could not have started any other way.\n\nThere did not have to be a perfect answer. There only had to be a place to start.", "my": "အတွေ့အကြုံအချို့ဟာ ဖော်ပြစရာ စကားလုံးတွေ မရှိခင်မှာပဲ ရောက်လာတတ်ပါတယ်။ ဒီပြန်လည်ဆင်ခြင်မှုဟာ အရင်ဖန်တီးပြီး နောက်မှ ရှင်းပြခွင့် ရတဲ့အခါ ဘာတွေ ပြောင်းလဲသွားလဲဆိုတာကို သတိထားမိခြင်းနဲ့ စတင်ပါတယ်။\n\nဆက်ရှင်တစ်ခုမှာ တစ်စုံတစ်ယောက်က စက္ကူတစ်ရွက်ကို ကောက်ယူပြီး ဆုတ်လိုက်ပါတယ်။ စိတ်ပျက်လို့ မဟုတ်ပါဘူး — စူးစမ်းချင်လို့ပါ။ သူက စက္ကူအစွန်းတွေကို ကိုင်ပြီး ဆုတ်ရာကနေ ဖြစ်ပေါ်လာတာကို ကြည့်နေခဲ့ပါတယ်။ ကြိုတင် စီစဉ်ထားတာ မဟုတ်ပါဘူး။ ဒီအတိုင်း ဖြစ်သွားတာပါ။\n\nအဲဒီနောက် ဖြစ်လာတာကတော့ တခြားနည်းနဲ့ ဘယ်လိုမှ စတင်နိုင်မှာ မဟုတ်တဲ့ စကားပြောဆိုမှုတစ်ခုပါ။\n\nပြီးပြည့်စုံတဲ့ အဖြေ ရှိစရာ မလိုခဲ့ပါဘူး။ စတင်စရာ နေရာတစ်ခု ရှိဖို့သာ လိုခဲ့တာပါ။"}'
FROM post;

WITH post AS (
    INSERT INTO posts (title, kind)
    SELECT 'When art holds what words cannot carry', 'insight'
    WHERE NOT EXISTS (SELECT 1 FROM post_versions WHERE channel = 'website' AND slug = 'art-and-words')
    RETURNING id
)
INSERT INTO post_versions (post_id, channel, enabled, slug, title, excerpt, body)
SELECT id, 'website', true, 'art-and-words',
       '{"en": "When art holds what words cannot carry", "my": "စကားလုံးတွေ မသယ်ဆောင်နိုင်တာကို အနုပညာက ထိန်းထားပေးတဲ့အခါ"}',
       '{"en": "Sometimes the things we most need to say do not arrive as sentences. They arrive as colour, weight, pressure on paper.", "my": "တစ်ခါတလေ ကျွန်ုပ်တို့ ပြောဖို့ အလိုအပ်ဆုံး အရာတွေဟာ ဝါကျတွေအဖြစ် ရောက်မလာပါဘူး။ အရောင်၊ အလေးချိန်၊ စက္ကူပေါ်က ဖိအားအဖြစ် ရောက်လာတတ်ပါတယ်။"}',
       NULL
FROM post;

WITH post AS (
    INSERT INTO posts (title, kind)
    SELECT 'What creativity can bring into a healthcare space', 'insight'
    WHERE NOT EXISTS (SELECT 1 FROM post_versions WHERE channel = 'website' AND slug = 'creativity-in-healthcare')
    RETURNING id
)
INSERT INTO post_versions (post_id, channel, enabled, slug, title, excerpt, body)
SELECT id, 'website', true, 'creativity-in-healthcare',
       '{"en": "What creativity can bring into a healthcare space", "my": "ဖန်တီးမှုက ကျန်းမာရေး စောင့်ရှောက်မှု နေရာထဲကို ဘာတွေ ယူဆောင်လာနိုင်မလဲ"}',
       '{"en": "A reflection on what happens when art is offered in places of waiting, uncertainty and care.", "my": "စောင့်ဆိုင်းရတဲ့ နေရာ၊ မသေချာမရေရာမှုတွေ ရှိတဲ့ နေရာ၊ စောင့်ရှောက်မှု ပေးတဲ့ နေရာတွေမှာ အနုပညာကို ကမ်းလှမ်းလိုက်တဲ့အခါ ဘာတွေ ဖြစ်လာသလဲ ဆိုတာအကြောင်း ပြန်လည်ဆင်ခြင်မှုတစ်ခုပါ။"}',
       NULL
FROM post;

-- +goose Down
DELETE FROM posts p
USING post_versions v
WHERE v.post_id = p.id AND v.channel = 'website' AND p.status = 'draft'
  AND v.slug IN ('stop-trying-make-perfect', 'art-and-words', 'creativity-in-healthcare');
