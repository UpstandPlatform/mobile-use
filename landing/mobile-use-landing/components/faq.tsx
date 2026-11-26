"use client"

import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from "@/components/ui/accordion"

const questions = [
  {
    value: "devices",
    question: "Which devices can I connect?",
    answer:
      "Android connects over USB or Wireless Debugging, or through the companion for approved screen sharing and sensors. iPhone support uses a Mac with Xcode, Appium, and a signed WebDriverAgent.",
  },
  {
    value: "control",
    question: "Can the companion tap and type on its own?",
    answer:
      "The companion handles screen sharing and approved camera or microphone requests. Touch, keyboard, app discovery, and app installation use an authorized ADB connector on Android, or the supported Appium connector on iPhone.",
  },
  {
    value: "local",
    question: "Can everything stay on my computer?",
    answer:
      "Yes. `mobile-use up` starts the local service, prepares the workstation pairing flow, and opens the console. A private remote deployment is available when devices or agents need to connect from elsewhere.",
  },
  {
    value: "approval",
    question: "How does device approval work?",
    answer:
      "Personal devices ask their owner to approve each session and permission. Dedicated devices can use explicit grants for unattended work. Sessions are scoped, visible in the console, and recorded in the audit log.",
  },
]

export function Faq() {
  return (
    <Accordion className="faq-list" defaultValue={["devices"]}>
      {questions.map((item) => (
        <AccordionItem key={item.value} value={item.value}>
          <AccordionTrigger className="faq-trigger">{item.question}</AccordionTrigger>
          <AccordionContent className="faq-answer">{item.answer}</AccordionContent>
        </AccordionItem>
      ))}
    </Accordion>
  )
}
